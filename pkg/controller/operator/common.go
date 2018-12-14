package operator

import (
	"fmt"
	"reflect"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"k8s.io/apimachinery/pkg/types"

	"math"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/pkg/errors"
	"github.com/spf13/cast"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
)

// This is a collection of some common methods that may be shared by operator code

// NewDefaultPodSpec creates default pod spec, seems we shouldn't set CPU and Memory if they are not provided by user
func NewDefaultPodSpec() mongodb.MongoDbPodSpec {
	defaultPodSpec := mongodb.MongoDbPodSpecStandard{}
	defaultPodSpec.Persistence = &mongodb.Persistence{
		SingleConfig: &mongodb.PersistenceConfig{Storage: util.DefaultMongodStorageSize},
		MultipleConfig: &mongodb.MultiplePersistenceConfig{
			Data:    &mongodb.PersistenceConfig{Storage: util.DefaultMongodStorageSize},
			Journal: &mongodb.PersistenceConfig{Storage: util.DefaultJournalStorageSize},
			Logs:    &mongodb.PersistenceConfig{Storage: util.DefaultLogsStorageSize},
		},
	}

	return mongodb.MongoDbPodSpec{
		MongoDbPodSpecStandard:     defaultPodSpec,
		PodAntiAffinityTopologyKey: util.DefaultAntiAffinityTopologyKey,
	}
}

// NewDefaultPodSpecWrapper
func NewDefaultPodSpecWrapper(podSpec mongodb.MongoDbPodSpec) mongodb.PodSpecWrapper {
	return mongodb.PodSpecWrapper{
		MongoDbPodSpec: podSpec,
		Default:        NewDefaultPodSpec(),
	}
}

// NewDefaultStandalonePodSpecWrapper
func NewDefaultStandalonePodSpecWrapper(podSpec mongodb.MongoDbPodSpecStandard) mongodb.PodSpecWrapper {
	return mongodb.PodSpecWrapper{
		MongoDbPodSpec: mongodb.MongoDbPodSpec{MongoDbPodSpecStandard: podSpec},
		Default:        NewDefaultPodSpec(),
	}
}

func buildReplicaSetFromStatefulSet(set *appsv1.StatefulSet, clusterName, version string) om.ReplicaSetWithProcesses {
	members := createProcesses(set, clusterName, version, om.ProcessTypeMongod)
	rsWithProcesses := om.NewReplicaSetWithProcesses(om.NewReplicaSet(set.Name, version), members)
	return rsWithProcesses
}

func createProcesses(set *appsv1.StatefulSet, clusterName, version string, mongoType om.MongoType) []om.Process {
	hostnames, names := GetDnsForStatefulSet(set, clusterName)
	processes := make([]om.Process, len(hostnames))
	wiredTigerCache := calculateWiredTigerCache(set)

	for idx, hostname := range hostnames {
		switch mongoType {
		case om.ProcessTypeMongod:
			processes[idx] = om.NewMongodProcess(names[idx], hostname, version)
			if wiredTigerCache != nil {
				processes[idx].SetWiredTigerCache(*wiredTigerCache)
			}
		case om.ProcessTypeMongos:
			processes[idx] = om.NewMongosProcess(names[idx], hostname, version)
		default:
			panic("Dev error: Wrong process type passed!")
		}
	}

	return processes
}

// calculateWiredTigerCache returns the cache that needs to be dedicated to mongodb engine. May be we won't need this manual
// setting any more when SERVER-16571 is fixed
func calculateWiredTigerCache(set *appsv1.StatefulSet) *float32 {
	// Note, that if the limit is 0 then it's not specified in fact (unbounded)
	if memory := set.Spec.Template.Spec.Containers[0].Resources.Limits.Memory(); memory != nil && (*memory).Value() != 0 {
		// Value() returns size in bytes so we need to transform to Gigabytes
		wt := cast.ToFloat64((*memory).Value()) / 1000000000
		// https://docs.mongodb.com/manual/core/wiredtiger/#memory-use
		wt = math.Max((wt-1)*0.5, 0.256)
		// rounding fractional part to 3 digits
		rounded := float32(math.Floor(wt*1000) / 1000)
		return &rounded
	}
	return nil
}

func waitForRsAgentsToRegister(set *appsv1.StatefulSet, clusterName string, omConnection om.Connection, log *zap.SugaredLogger) error {
	hostnames, _ := GetDnsForStatefulSet(set, clusterName)
	log = log.With("statefulset", set.Name)

	if !waitUntilAgentsHaveRegistered(omConnection, log, hostnames...) {
		return errors.New("Some agents failed to register")
	}
	return nil
}

// waitUntilAgentsHaveRegistered waits until all agents with 'agentHostnames' are registered in OM. Note, that wait
// happens after retrial - this allows to skip waiting in case agents are already registered
func waitUntilAgentsHaveRegistered(omConnection om.Connection, log *zap.SugaredLogger, agentHostnames ...string) bool {
	log.Infow("Waiting for agents to register with OM", "agent hosts", agentHostnames)
	waitSeconds := util.ReadEnvVarOrPanicInt(util.PodWaitSecondsEnv)
	retrials := util.ReadEnvVarOrPanicInt(util.PodWaitRetriesEnv)

	agentsCheckFunc := func() (string, bool) {
		agentResponse, err := omConnection.ReadAutomationAgents()
		if err != nil {
			return fmt.Sprintf("Unable to read from OM API: %s", err), false
		}

		registeredCount := 0
		for _, hostname := range agentHostnames {
			if om.CheckAgentExists(hostname, agentResponse, log) {
				registeredCount++
			}
		}

		if registeredCount == len(agentHostnames) {
			return "", true
		}
		var msg string
		if registeredCount == 0 {
			msg = fmt.Sprintf("None of %d agents has registered with OM", len(agentHostnames))
		} else {
			msg = fmt.Sprintf("Only %d of %d agents have registered with OM", registeredCount, len(agentHostnames))
		}
		return msg, false
	}

	return util.DoAndRetry(agentsCheckFunc, log, retrials, waitSeconds)
}

// prepareScaleDown performs additional steps necessary to make sure removed members are not primary (so no
// election happens and replica set is available) (see
// https://jira.mongodb.org/browse/HELP-3818?focusedCommentId=1548348 for more details)
// Note, that we are skipping setting nodes as "disabled" (but the code is commented to be able to revert this if
// needed)
func prepareScaleDown(omClient om.Connection, rsMembers map[string][]string, log *zap.SugaredLogger) error {
	processes := make([]string, 0)
	for _, v := range rsMembers {
		processes = append(processes, v...)
	}

	// Stage 1. Set Votes and Priority to 0
	if len(rsMembers) > 0 {
		err := omClient.ReadUpdateDeployment(true,
			func(d om.Deployment) error {
				for k, v := range rsMembers {
					if err := d.MarkRsMembersUnvoted(k, v); err != nil {
						log.Errorf("Problems scaling down some replica sets (were they changed in Ops Manager directly?): %s", err)
					}
				}
				return nil
			},
			log,
		)

		if err != nil {
			return fmt.Errorf("Unable to set votes, priority to 0 in Ops Manager, hosts: %v, err: %s", processes, err)
		}

		log.Debugw("Marked replica set members as non-voting", "replica set with members", rsMembers)
	}

	// TODO practice shows that automation agents can get stuck on setting db to "disabled" also it seems that this process
	// works correctly without explicit disabling - feel free to remove this code after some time when it is clear
	// that everything works correctly without disabling

	// Stage 2. Set disabled to true
	//err = omClient.ReadUpdateDeployment(true,
	//	func(d om.Deployment) error {
	//		d.DisableProcesses(allProcesses)
	//		return nil
	//	},
	//)
	//
	//if err != nil {
	//	return errors.New(fmt.Sprintf("Unable to set disabled to true, hosts: %v, err: %s", allProcesses, err))
	//}
	//log.Debugw("Disabled processes", "processes", allProcesses)

	log.Infow("Performed some preliminary steps to support scale down", "hosts", processes)

	return nil
}

// stopMonitoringHosts removes monitoring for this list of hosts from Ops Manager.
func stopMonitoringHosts(conn om.Connection, hosts []string, log *zap.SugaredLogger) error {
	if len(hosts) == 0 {
		return nil
	}

	if err := om.StopMonitoring(conn, hosts, log); err != nil {
		return fmt.Errorf("Failed to stop monitoring on hosts %s: %s", hosts, err)
	}

	return nil
}

// calculateDiffAndStopMonitoringHosts checks hosts that are present in hostsBefore but not hostsAfter, and removes
// monitoring from them.
func calculateDiffAndStopMonitoringHosts(conn om.Connection, hostsBefore, hostsAfter []string, log *zap.SugaredLogger) error {
	return stopMonitoringHosts(conn, util.FindLeftDifference(hostsBefore, hostsAfter), log)
}

// agentApiKeySecretName for a given ProjectID (`project`) returns the name of
// the secret associated with it.
func agentApiKeySecretName(project string) string {
	return fmt.Sprintf("%s-group-secret", project)
}

// completionMessage is just a general message printed in the logs after mongodb resource is created/updated
func completionMessage(url, groupID string) string {
	return fmt.Sprintf("Please check the link %s/v2/%s to see the status of the deployment", url, groupID)
}

// exceptionHandling is the basic panic handling function that recovers from panic, logs the error, updates the resource status and updates the
// reconcile result and error parameters (as reconcile logic will return it later)
// passing result and error as an argument and updating the pointer of it didn't work (thanks Go), had to use ugly function
func exceptionHandling(errHandlingFunc func() (reconcile.Result, error), errUpdateFunc func(res reconcile.Result, err error)) {
	if r := recover(); r != nil {
		result, e := errHandlingFunc()

		errUpdateFunc(result, e)
	}
}

// objectKey creates the 'client.ObjectKey' object from namespace and name of the resource. It's the object used in
// some of 'client.Client' calls
func objectKey(ns, name string) client.ObjectKey {
	return types.NamespacedName{Namespace: ns, Name: name}
}

func objectKeyFromApiObject(obj interface{}) client.ObjectKey {
	ns := reflect.ValueOf(obj).Elem().FieldByName("Namespace").String()
	name := reflect.ValueOf(obj).Elem().FieldByName("Name").String()

	return objectKey(ns, name)
}
