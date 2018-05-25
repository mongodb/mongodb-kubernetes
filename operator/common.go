package operator

import (
	"time"

	"github.com/10gen/ops-manager-kubernetes/om"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1beta1"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
)

// This is a collection of some utility/common methods that may be shared by other go source code

// Int32Ref is required to return a *int32, which can't be declared as a literal.
func Int32Ref(i int32) *int32 {
	return &i
}

// BooleanRef is required to return a *bool, which can't be declared as a literal.
func BooleanRef(b bool) *bool {
	return &b
}

// DoAndRetry performs the task 'f' until it returns true or 'count' retrials are executed. Sleeps for 'interval' seconds
// between retries
func DoAndRetry(f func() bool, log *zap.SugaredLogger, count, interval int) bool {
	for i := 0; i < count; i++ {
		if f() {
			return true
		}
		// if we are on the last iteration - returning as there's no need to wait and retry again
		if i != count-1 {
			log.Debugf("Retrial attempt %d of %d (waiting for %d more seconds)", i+2, count, interval)
			time.Sleep(time.Duration(interval) * time.Second)
		}
	}
	return false
}

// NewDefaultPodSpec creates default pod spec, seems we shouldn't set CPU and Memory if they are not provided by user
func NewDefaultPodSpec() mongodb.MongoDbPodSpec {
	return mongodb.MongoDbPodSpec{
		MongoDbPodSpecStandalone:   mongodb.MongoDbPodSpecStandalone{Storage: DefaultMongodStorageSize},
		PodAntiAffinityTopologyKey: "kubernetes.io/hostname"}
}

func buildReplicaSetFromStatefulSet(set *appsv1.StatefulSet, clusterName, version string) om.ReplicaSetWithProcesses {
	members := createProcesses(set, clusterName, version, om.ProcessTypeMongod)
	rsWithProcesses := om.NewReplicaSetWithProcesses(om.NewReplicaSet(set.Name), members)
	return rsWithProcesses
}

func createProcesses(set *appsv1.StatefulSet, clusterName, version string, mongoType om.MongoType) []om.Process {
	hostnames, names := GetDnsForStatefulSet(set, clusterName)
	processes := make([]om.Process, len(hostnames))

	for idx, hostname := range hostnames {
		switch mongoType {
		case om.ProcessTypeMongod:
			processes[idx] = om.NewMongodProcess(names[idx], hostname, version)
		case om.ProcessTypeMongos:
			processes[idx] = om.NewMongosProcess(names[idx], hostname, version)
		default:
			panic("Dev error: Wrong process type passed!")
		}
	}

	return processes
}

func waitForRsAgentsToRegister(set *appsv1.StatefulSet, clusterName string, omConnection *om.OmConnection, log *zap.SugaredLogger) error {
	hostnames, _ := GetDnsForStatefulSet(set, clusterName)
	if !waitUntilAgentsHaveRegistered(omConnection, log, hostnames...) {
		return errors.New("Some agents failed to register.")
	}
	return nil
}

// waitUntilAgentsHaveRegistered waits until all agents with 'agentHostnames' are registered in OM. It waits for 1 minute
// and retries eac 3 seconds. Note, that wait happens after retrial - this allows skip waiting in case agents are already
// registered
func waitUntilAgentsHaveRegistered(omConnection *om.OmConnection, log *zap.SugaredLogger, agentHostnames ...string) bool {
	log.Infow("Waiting for agents to register with OM", "agent hosts", agentHostnames)
	agentsCheckFunc := func() bool {
		agentResponse, err := omConnection.ReadAutomationAgents()
		if err != nil {
			log.Error("Unable to read from OM API: ", err)
			return false
		}

		registeredCount := 0
		for _, hostname := range agentHostnames {
			if om.CheckAgentExists(hostname, agentResponse, log) {
				registeredCount++
			}
		}

		if registeredCount == len(agentHostnames) {
			return true
		}
		if registeredCount == 0 {
			log.Infof("None of %d agents has registered with OM", len(agentHostnames))
		} else {
			log.Infof("Only %d of %d agents have registered with OM", registeredCount, len(agentHostnames))
		}
		return false
	}

	return DoAndRetry(agentsCheckFunc, log, 20, 3)
}
