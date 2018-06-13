package operator

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/om"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/util"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
)

// This is a collection of some common methods that may be shared by operator code

// NewDefaultPodSpec creates default pod spec, seems we shouldn't set CPU and Memory if they are not provided by user
func NewDefaultPodSpec() mongodb.MongoDbPodSpec {
	return mongodb.MongoDbPodSpec{
		MongoDbPodSpecStandalone:   mongodb.MongoDbPodSpecStandalone{Storage: DefaultMongodStorageSize},
		PodAntiAffinityTopologyKey: DefaultAntiAffinityTopologyKey}
}

func NewDefaultPodSpecWrapper(podSpec mongodb.MongoDbPodSpec) mongodb.PodSpecWrapper {
	return mongodb.PodSpecWrapper{
		MongoDbPodSpec: podSpec,
		Default:        NewDefaultPodSpec(),
	}
}

func NewDefaultStandalonePodSpecWrapper(podSpec mongodb.MongoDbPodSpecStandalone) mongodb.PodSpecWrapper {
	return mongodb.PodSpecWrapper{
		MongoDbPodSpec: mongodb.MongoDbPodSpec{MongoDbPodSpecStandalone: podSpec},
		Default:        NewDefaultPodSpec(),
	}
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

func waitForRsAgentsToRegister(set *appsv1.StatefulSet, clusterName string, omConnection om.OmConnection, log *zap.SugaredLogger) error {
	hostnames, _ := GetDnsForStatefulSet(set, clusterName)
	log = log.With("statefulset", set.Name)

	if !waitUntilAgentsHaveRegistered(omConnection, log, hostnames...) {
		return errors.New("Some agents failed to register.")
	}
	return nil
}

// waitUntilAgentsHaveRegistered waits until all agents with 'agentHostnames' are registered in OM. It waits for 1 minute
// and retries eac 3 seconds. Note, that wait happens after retrial - this allows skip waiting in case agents are already
// registered
func waitUntilAgentsHaveRegistered(omConnection om.OmConnection, log *zap.SugaredLogger, agentHostnames ...string) bool {
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

	return util.DoAndRetry(agentsCheckFunc, log, 20, 3)
}

// prepareScaleDown performs additional steps necessary to make sure removed members are not primary (so no
// election happens and replica set is available) (see
// https://jira.mongodb.org/browse/HELP-3818?focusedCommentId=1548348 for more details)
// Note, that we are skipping setting nodes as "disabled" (but the code is commented to be able to revert this if
// needed)
func prepareScaleDown(omClient om.OmConnection, rsMembers map[string][]string, log *zap.SugaredLogger) error {
	allProcesses := make([]string, 0)
	for _, v := range rsMembers {
		allProcesses = append(allProcesses, v...)
	}

	// Stage 1. Set Votes and Priority to 0
	if len(rsMembers) > 0 {
		err := omClient.ReadUpdateDeployment(true,
			func(d om.Deployment) error {
				for k, v := range rsMembers {
					d.MarkRsMembersUnvoted(k, v)
				}
				return nil
			},
		)

		if err != nil {
			return errors.New(fmt.Sprintf("Unable to set votes, priority to 0, hosts: %v, err: %s", allProcesses, err))
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

	log.Infow("Performed some preliminary steps to support scale down", "hosts", allProcesses)

	return nil
}

// deleteHostnamesFromMonitoring checks the array of hosts before change to Deployment and after and if some hosts
// were removed from Kubernetes/OM Deployment - then we need to explicitly remove these hosts from OM monitoring
func deleteHostnamesFromMonitoring(omClient om.OmConnection, hostsBefore, hostsAfter []string, log *zap.SugaredLogger) error {
	diff := util.FindLeftDifference(hostsBefore, hostsAfter)

	if len(diff) > 0 {
		if err := om.StopMonitoring(omClient, diff); err != nil {
			return errors.New(fmt.Sprintf("Failed to stop monitoring on hosts %s: %s", diff, err))
		}
		log.Infow("Removed hosts from monitoring in Ops Manager", "hosts", diff)
	}
	return nil
}

// agentApiKeySecretName for a given ProjectID (`project`) returns the name of
// the secret associated with it.
func agentApiKeySecretName(project string) string {
	return fmt.Sprintf("%s-group-secret", project)
}
