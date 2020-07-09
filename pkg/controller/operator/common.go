package operator

import (
	"fmt"
	"math"
	"sync"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/host"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/envutil"
	"github.com/pkg/errors"
	"github.com/spf13/cast"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// This is a collection of some common methods that may be shared by operator code

// NewDefaultPodSpec creates pod spec with default values,sets only the topology key and persistence sizes,
// seems we shouldn't set CPU and Memory if they are not provided by user
func NewDefaultPodSpec() mdbv1.MongoDbPodSpec {
	podSpecWrapper := mdbv1.NewEmptyPodSpecWrapperBuilder().
		SetPodAntiAffinityTopologyKey(util.DefaultAntiAffinityTopologyKey).
		SetSinglePersistence(mdbv1.NewPersistenceBuilder(util.DefaultMongodStorageSize)).
		SetMultiplePersistence(mdbv1.NewPersistenceBuilder(util.DefaultMongodStorageSize),
			mdbv1.NewPersistenceBuilder(util.DefaultJournalStorageSize),
			mdbv1.NewPersistenceBuilder(util.DefaultLogsStorageSize)).
		Build()

	return podSpecWrapper.MongoDbPodSpec
}

// NewDefaultPodSpecWrapper
func NewDefaultPodSpecWrapper(podSpec mdbv1.MongoDbPodSpec) *mdbv1.PodSpecWrapper {
	return &mdbv1.PodSpecWrapper{
		MongoDbPodSpec: podSpec,
		Default:        NewDefaultPodSpec(),
	}
}

func DeploymentLink(url, groupId string) string {
	return fmt.Sprintf("%s/v2/%s", url, groupId)
}

func buildReplicaSetFromStatefulSet(set appsv1.StatefulSet, mdb *mdbv1.MongoDB) om.ReplicaSetWithProcesses {
	members := createMongodProcesses(set, mdb)
	replicaSet := om.NewReplicaSet(set.Name, mdb.Spec.GetVersion())
	rsWithProcesses := om.NewReplicaSetWithProcesses(replicaSet, members)
	rsWithProcesses.SetHorizons(mdb.Spec.Connectivity.ReplicaSetHorizons)
	return rsWithProcesses
}

// createMongodProcesses builds the slice of processes based on 'StatefulSet' and 'MongoDB' spec.
// Note, that it's not applicable for sharded cluster processes as each of them may have their own mongod
// options configuration, also mongos process is different
func createMongodProcesses(set appsv1.StatefulSet, mdb *mdbv1.MongoDB) []om.Process {
	hostnames, names := util.GetDnsForStatefulSet(set, mdb.Spec.GetClusterDomain())
	processes := make([]om.Process, len(hostnames))
	wiredTigerCache := calculateWiredTigerCache(set, mdb.Spec.GetVersion())

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongodProcess(names[idx], hostname, mdb.Spec.AdditionalMongodConfig, mdb)
		if wiredTigerCache != nil {
			processes[idx].SetWiredTigerCache(*wiredTigerCache)
		}
	}

	return processes
}

// calculateWiredTigerCache returns the cache that needs to be dedicated to mongodb engine.
// This was fixed in SERVER-16571 so we don't need to enable this for some latest version of mongodb (see the ticket)
func calculateWiredTigerCache(set appsv1.StatefulSet, version string) *float32 {
	shouldCalculate, err := util.VersionMatchesRange(version, ">=4.0.0 <4.0.9 || <3.6.13")

	if err != nil || shouldCalculate {
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
	}
	return nil
}

func waitForRsAgentsToRegister(set appsv1.StatefulSet, clusterName string, omConnection om.Connection, log *zap.SugaredLogger) error {
	hostnames, _ := util.GetDnsForStatefulSet(set, clusterName)
	log = log.With("statefulset", set.Name)

	if !waitUntilAgentsHaveRegistered(omConnection, log, hostnames...) {
		return errors.New("Some agents failed to register or the Operator is using the wrong host names for the pods. " +
			"Make sure the 'spec.clusterDomain' is set if it's different from the default Kubernetes cluster " +
			"name ('cluster.local') ")
	}
	return nil
}

// waitUntilAgentsHaveRegistered waits until all agents with 'agentHostnames' are registered in OM. Note, that wait
// happens after retrial - this allows to skip waiting in case agents are already registered
func waitUntilAgentsHaveRegistered(omConnection om.Connection, log *zap.SugaredLogger, agentHostnames ...string) bool {
	log.Infow("Waiting for agents to register with OM", "agent hosts", agentHostnames)
	// environment variables are used only for tests
	waitSeconds := envutil.ReadIntOrDefault(util.PodWaitSecondsEnv, 3)
	retrials := envutil.ReadIntOrDefault(util.PodWaitRetriesEnv, 5)

	agentsCheckFunc := func() (string, bool) {
		registeredCount := 0
		found, err := om.TraversePages(
			omConnection.ReadAutomationAgents,
			func(aa interface{}) bool {
				automationAgent := aa.(om.AgentStatus)

				for _, hostname := range agentHostnames {
					if automationAgent.IsRegistered(hostname, log) {
						registeredCount++
						if registeredCount == len(agentHostnames) {
							return true
						}
					}
				}
				return false
			},
		)

		if err != nil {
			log.Errorw("Received error when reading automation agent pages", "err", err)
		}

		var msg string
		if registeredCount == 0 {
			msg = fmt.Sprintf("None of %d agents has registered with OM", len(agentHostnames))
		} else {
			msg = fmt.Sprintf("Only %d of %d agents have registered with OM", registeredCount, len(agentHostnames))
		}
		return msg, found
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
		err := omClient.ReadUpdateDeployment(
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

		if err := om.WaitForReadyState(omClient, processes, log); err != nil {
			return err
		}

		log.Debugw("Marked replica set members as non-voting", "replica set with members", rsMembers)
	}

	// TODO practice shows that automation agents can get stuck on setting db to "disabled" also it seems that this process
	// works correctly without explicit disabling - feel free to remove this code after some time when it is clear
	// that everything works correctly without disabling

	// Stage 2. Set disabled to true
	//err = omClient.ReadUpdateDeployment(
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
func stopMonitoringHosts(getRemover host.GetRemover, hosts []string, log *zap.SugaredLogger) error {
	if len(hosts) == 0 {
		return nil
	}

	if err := host.StopMonitoring(getRemover, hosts, log); err != nil {
		return fmt.Errorf("Failed to stop monitoring on hosts %s: %s", hosts, err)
	}

	return nil
}

// calculateDiffAndStopMonitoringHosts checks hosts that are present in hostsBefore but not hostsAfter, and removes
// monitoring from them.
func calculateDiffAndStopMonitoringHosts(getRemover host.GetRemover, hostsBefore, hostsAfter []string, log *zap.SugaredLogger) error {
	return stopMonitoringHosts(getRemover, util.FindLeftDifference(hostsBefore, hostsAfter), log)
}

// agentApiKeySecretName for a given ProjectID (`project`) returns the name of
// the secret associated with it.
func agentApiKeySecretName(project string) string {
	return fmt.Sprintf("%s-group-secret", project)
}

// completionMessage is just a general message printed in the logs after mongodb resource is created/updated
func completionMessage(url, projectID string) string {
	return fmt.Sprintf("Please check the link %s/v2/%s to see the status of the deployment", url, projectID)
}

// exceptionHandling is the basic panic handling function that recovers from panic, logs the error, updates the resource status and updates the
// reconcile result and error parameters (as reconcile logic will return it later)
// passing result and error as an argument and updating the pointer of it didn't work (thanks Go), had to use ugly function
func exceptionHandling(errHandlingFunc func(err interface{}) (reconcile.Result, error), errUpdateFunc func(res reconcile.Result, err error)) {
	if r := recover(); r != nil {
		result, e := errHandlingFunc(r)
		errUpdateFunc(result, e)
	}
}

// objectKey creates the 'client.ObjectKey' object from namespace and name of the resource. It's the object used in
// some of 'client.Client' calls
// TODO move somewhere else (subpackage? external package?) to be able to reuse the method from everywhere
// Deprecated: use 'kube.ObjectKey' instead
func objectKey(namespace, name string) client.ObjectKey {
	return types.NamespacedName{Name: name, Namespace: namespace}
}

// MongoDBResourceEventHandler is a custom event handler that extends the
// handler.EnqueueRequestForObject event handler. It overrides the Delete
// method used to clean up the mongodb resource when a deletion event happens.
// This results in a single, synchronous attempt to clean up the resource
// rather than an asynchronous one.
type MongoDBResourceEventHandler struct {
	*handler.EnqueueRequestForObject
	reconciler interface {
		delete(obj interface{}, log *zap.SugaredLogger) error
		GetMutex(resourceName types.NamespacedName) *sync.Mutex
	}
}

func (eh *MongoDBResourceEventHandler) Delete(e event.DeleteEvent, _ workqueue.RateLimitingInterface) {
	objectKey := objectKey(e.Meta.GetNamespace(), e.Meta.GetName())
	logger := zap.S().With("resource", objectKey)

	// Reusing the lock used during update reconciliations
	mutex := eh.reconciler.GetMutex(objectKey)
	mutex.Lock()
	defer mutex.Unlock()

	zap.S().Infow("Cleaning up MongoDB resource", "resource", e.Object)
	if err := eh.reconciler.delete(e.Object, logger); err != nil {
		logger.Errorf("MongoDB resource removed from Kubernetes, but failed to clean some state in Ops Manager: %s", err)
		return
	}
	logger.Info("Removed MongoDB resource from Kubernetes and Ops Manager")
}

// toInternalClusterAuthName takes a hostname e.g. my-replica-set and converts
// it into the name of the secret which will hold the internal clusterFile
func toInternalClusterAuthName(name string) string {
	return fmt.Sprintf("%s-%s", name, util.ClusterFileName)
}

// operatorNamespace returns the current namespace where the Operator is deployed
func operatorNamespace() string {
	return envutil.ReadOrPanic(util.CurrentNamespace)
}

// runInGivenOrder will execute N functions, passed as varargs as `funcs`. The order of execution will depend on the result
// of the evaluation of the `shouldRunInOrder` boolean value. If `shouldRunInOrder` is true, the functions will be executed in order; if
// `shouldRunInOrder` is false, the functions will be executed in reverse order (from last to first)
func runInGivenOrder(shouldRunInOrder bool, funcs ...func() workflow.Status) workflow.Status {
	if shouldRunInOrder {
		for _, fn := range funcs {
			if status := fn(); !status.IsOK() {
				return status
			}
		}
	} else {
		for i := len(funcs) - 1; i >= 0; i-- {
			if status := funcs[i](); !status.IsOK() {
				return status
			}
		}
	}
	return workflow.OK()
}
