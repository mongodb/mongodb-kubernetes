package operator

import (
	"fmt"
	"math"
	"reflect"
	"time"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
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

// NewDefaultPodSpec creates default pod spec, seems we shouldn't set CPU and Memory if they are not provided by user
func NewDefaultPodSpec() mdbv1.MongoDbPodSpec {
	defaultPodSpec := mdbv1.MongoDbPodSpecStandard{}
	defaultPodSpec.Persistence = &mdbv1.Persistence{
		SingleConfig: &mdbv1.PersistenceConfig{Storage: util.DefaultMongodStorageSize},
		MultipleConfig: &mdbv1.MultiplePersistenceConfig{
			Data:    &mdbv1.PersistenceConfig{Storage: util.DefaultMongodStorageSize},
			Journal: &mdbv1.PersistenceConfig{Storage: util.DefaultJournalStorageSize},
			Logs:    &mdbv1.PersistenceConfig{Storage: util.DefaultLogsStorageSize},
		},
	}

	return mdbv1.MongoDbPodSpec{
		MongoDbPodSpecStandard:     defaultPodSpec,
		PodAntiAffinityTopologyKey: util.DefaultAntiAffinityTopologyKey,
	}
}

// NewDefaultPodSpecWrapper
func NewDefaultPodSpecWrapper(podSpec mdbv1.MongoDbPodSpec) mdbv1.PodSpecWrapper {
	return mdbv1.PodSpecWrapper{
		MongoDbPodSpec: podSpec,
		Default:        NewDefaultPodSpec(),
	}
}

// NewDefaultStandalonePodSpecWrapper
func NewDefaultStandalonePodSpecWrapper(podSpec mdbv1.MongoDbPodSpecStandard) mdbv1.PodSpecWrapper {
	return mdbv1.PodSpecWrapper{
		MongoDbPodSpec: mdbv1.MongoDbPodSpec{MongoDbPodSpecStandard: podSpec},
		Default:        NewDefaultPodSpec(),
	}
}

func DeploymentLink(url, groupId string) string {
	return fmt.Sprintf("%s/v2/%s", url, groupId)
}

func buildReplicaSetFromStatefulSet(set *appsv1.StatefulSet, mdb *mdbv1.MongoDB, log *zap.SugaredLogger) om.ReplicaSetWithProcesses {
	members := createProcesses(set, om.ProcessTypeMongod, mdb, log)
	replicaSet := om.NewReplicaSet(set.Name, mdb.Spec.Version)
	rsWithProcesses := om.NewReplicaSetWithProcesses(replicaSet, members)
	rsWithProcesses.SetHorizons(mdb.Spec.Connectivity.ReplicaSetHorizons)
	rsWithProcesses.ConfigureAuthenticationMode(mdb.Spec.Security.Authentication.InternalCluster)
	return rsWithProcesses
}

func createProcesses(set *appsv1.StatefulSet, mongoType om.MongoType,
	mdb *mdbv1.MongoDB, log *zap.SugaredLogger) []om.Process {

	hostnames, names := GetDnsForStatefulSet(set, mdb.Spec.ClusterName)
	processes := make([]om.Process, len(hostnames))
	wiredTigerCache := calculateWiredTigerCache(set, mdb.Spec.Version)

	for idx, hostname := range hostnames {
		switch mongoType {
		case om.ProcessTypeMongod:
			processes[idx] = om.NewMongodProcess(names[idx], hostname, mdb)
			if wiredTigerCache != nil {
				processes[idx].SetWiredTigerCache(*wiredTigerCache)
			}
		case om.ProcessTypeMongos:
			processes[idx] = om.NewMongosProcess(names[idx], hostname, mdb)
		default:
			panic("Dev error: Wrong process type passed!")
		}
	}

	return processes
}

// calculateWiredTigerCache returns the cache that needs to be dedicated to mongodb engine.
// This was fixed in SERVER-16571 so we don't need to enable this for some latest version of mongodb (see the ticket)
func calculateWiredTigerCache(set *appsv1.StatefulSet, version string) *float32 {
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

func waitForRsAgentsToRegister(set *appsv1.StatefulSet, clusterName string, omConnection om.Connection, log *zap.SugaredLogger) error {
	hostnames, _ := GetDnsForStatefulSet(set, clusterName)
	log = log.With("statefulset", set.Name)

	if !waitUntilAgentsHaveRegistered(omConnection, log, hostnames...) {
		return errors.New("Some agents failed to register or the Operator is using the wrong host names for the pods. " +
			"Make sure the 'spec.clusterName' is set if it's different from the default Kubernetes cluster " +
			"name ('cluster.local') ")
	}
	return nil
}

// waitUntilAgentsHaveRegistered waits until all agents with 'agentHostnames' are registered in OM. Note, that wait
// happens after retrial - this allows to skip waiting in case agents are already registered
func waitUntilAgentsHaveRegistered(omConnection om.Connection, log *zap.SugaredLogger, agentHostnames ...string) bool {
	log.Infow("Waiting for agents to register with OM", "agent hosts", agentHostnames)
	// environment variables are used only for tests
	waitSeconds := util.ReadEnvVarIntOrDefault(util.PodWaitSecondsEnv, 3)
	retrials := util.ReadEnvVarIntOrDefault(util.PodWaitRetriesEnv, 5)

	agentsCheckFunc := func() (string, bool) {
		registeredCount := 0
		pageNum := 0
		for pageNum >= 0 {
			agentResponse, err := omConnection.ReadAutomationAgents(pageNum)
			if err != nil {
				return fmt.Sprintf("Unable to read from OM API: %s", err), false
			}

			for _, hostname := range agentHostnames {
				if om.CheckAgentExists(hostname, agentResponse, log) {
					registeredCount++
				}
			}

			if registeredCount == len(agentHostnames) {
				return "", true
			} else {
				// printing extensive debug information only in case the agents were not found
				printDebuggingInformation(agentHostnames, agentResponse, log)
			}

			pageNum, err = om.FindNextPageForAgents(agentResponse)
			if err != nil {
				fmt.Println(err.Error())
			}
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

// printDebuggingInformation prints some debugging information which may help to find out the inconsistencies
// in names that agents report and the names that the Operator expects to see
func printDebuggingInformation(agentHostNames []string, agentResponse *om.AgentState, log *zap.SugaredLogger) {
	log.Debugf("The following agent host names were expected to be created in Ops Manager: %+v", agentHostNames)
	log.Debugf("The following agents are already registered in Ops Manager: %+v", agentResponse.Results)
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
func objectKey(namespace, name string) client.ObjectKey {
	return types.NamespacedName{Name: name, Namespace: namespace}
}

func objectKeyFromApiObject(obj interface{}) client.ObjectKey {
	ns := reflect.ValueOf(obj).Elem().FieldByName("Namespace").String()
	name := reflect.ValueOf(obj).Elem().FieldByName("Name").String()

	return objectKey(ns, name)
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
	}
}

func (eh *MongoDBResourceEventHandler) Delete(e event.DeleteEvent, _ workqueue.RateLimitingInterface) {
	zap.S().Infow("Cleaning up MongoDB resource", "resource", e.Object)
	logger := zap.S().With("resource", objectKey(e.Meta.GetNamespace(), e.Meta.GetName()))
	if err := eh.reconciler.delete(e.Object, logger); err != nil {
		logger.Errorf("MongoDB resource removed from Kubernetes, but failed to clean some state in Ops Manager: %s", err)
		return
	}
	logger.Info("Removed MongoDB resource from Kubernetes and Ops Manager")
}

// Reconciliation results returned during reconciliation

// success indicates we have successfully completed reconciliation
func success() (reconcile.Result, error) {
	return reconcile.Result{}, nil
}

// retry requeues the result after 10 seconds. If a non nil error is returned,
// the time is not respected and the event is immediately requeued.
func retry() (reconcile.Result, error) {
	return reconcile.Result{RequeueAfter: time.Second * 10}, nil
}

// stop is functionally identical to success (we stop trying) but exists to show intent.
func stop() (reconcile.Result, error) {
	return success()
}

// fail fails with the given error
func fail(err error) (reconcile.Result, error) {
	return reconcile.Result{}, err
}

// toInternalClusterAuthName takes a hostname e.g. my-replica-set and converts
// it into the name of the secret which will hold the internal clusterFile
func toInternalClusterAuthName(name string) string {
	return fmt.Sprintf("%s-%s", name, util.ClusterFileName)
}

// operatorNamespace returns the current namespace where the Operator is deployed
func operatorNamespace() string {
	return util.ReadEnvVarOrPanic(util.CurrentNamespace)
}

// runInGivenOrder will execute N functions, passed as varargs as `funcs`. The order of execution will depend on the result
// of the evaluation of the `shouldRunInOrder` boolean value. If `shouldRunInOrder` is true, the functions will be executed in order; if
// `shouldRunInOrder` is false, the functions will be executed in reverse order (from last to first)
func runInGivenOrder(shouldRunInOrder bool, funcs ...func() reconcileStatus) reconcileStatus {
	if shouldRunInOrder {
		for _, fn := range funcs {
			if status := fn(); !status.isOk() {
				return status
			}
		}
	} else {
		for i := len(funcs) - 1; i >= 0; i-- {
			if status := funcs[i](); !status.isOk() {
				return status
			}
		}
	}
	return ok()
}

type x509ConfigurationState struct {
	x509EnablingHasBeenRequested bool // if the desired state has x509 enabled
	shouldDisableX509            bool // if x509 should be disabled in this reconciliation
	x509CanBeEnabledInOpsManager bool // if Ops Manager is in a state in which it is possible to enable X509
}

func (x x509ConfigurationState) shouldLog() bool {
	return x.x509EnablingHasBeenRequested || x.shouldDisableX509
}

// getX509ConfigurationState returns information about what stages need to be performed when enabling x509 authentication
func getX509ConfigurationState(ac *om.AutomationConfig, authModes []string) x509ConfigurationState {
	// we only need to make the corresponding requests to configure x509 if we're enabling/disabling it
	// otherwise we don't need to make any changes.
	x509EnablingHasBeenRequested := !util.ContainsString(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigX509Option) && util.ContainsString(authModes, util.X509)
	shouldDisableX509 := util.ContainsString(ac.Auth.DeploymentAuthMechanisms, util.AutomationConfigX509Option) && !util.ContainsString(authModes, util.X509)
	return x509ConfigurationState{
		x509EnablingHasBeenRequested: x509EnablingHasBeenRequested,
		shouldDisableX509:            shouldDisableX509,
		x509CanBeEnabledInOpsManager: ac.Deployment.AllProcessesAreTLSEnabled() || ac.Deployment.NumberOfProcesses() == 0,
	}
}
