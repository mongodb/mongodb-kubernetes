package operator

import (
	"errors"
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/om"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	"go.uber.org/zap"
)

func (c *MongoDbController) onAddReplicaSet(obj interface{}) {
	s := obj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	log := zap.S().With("replicaSet", s.Name)

	log.Infow("Creating Replica set", "config", s.Spec)

	conn, err := c.getOmConnection(s.Namespace, s.Spec.OmConfigName)
	if err != nil {
		log.Errorf("Failed to read OpsManager config map %s: %s", s.Spec.OmConfigName, err)
		return
	}

	agentKeySecretName, err := c.EnsureAgentKeySecretExists(conn, s.Namespace)

	if err != nil {
		log.Error("Failed to generate/get agent key: ", err)
		return
	}

	replicaSetObject := buildReplicaSetStatefulSet(s, agentKeySecretName)
	_, err = c.kubeHelper.createOrUpdateStatefulsetsWithService(s.Spec.Service, 27017, s.Namespace, true, replicaSetObject)
	if err != nil {
		log.Error("Error trying to create a new statefulset and services for ReplicaSet: ", err)
		return
	}

	if err := c.updateOmDeploymentRs(conn, nil, s); err != nil {
		log.Error("Failed to update OpsManager automation config: ", err)
		return
	}

	log.Info("Created Replica Set!")
}

func (c *MongoDbController) onUpdateReplicaSet(oldObj, newObj interface{}) {
	oldRes := oldObj.(*mongodb.MongoDbReplicaSet).DeepCopy()
	newRes := newObj.(*mongodb.MongoDbReplicaSet).DeepCopy()
	log := zap.S().With("replicaSet", newRes.Name)

	if err := validateUpdate(oldRes, newRes); err != nil {
		log.Error(err)
		return
	}

	log.Infow("Updating MongoDbReplicaSet", "oldConfig", oldRes.Spec, "newConfig", newRes.Spec)

	conn, err := c.getOmConnection(newRes.Namespace, newRes.Spec.OmConfigName)
	if err != nil {
		log.Errorf("Failed to read OpsManager config map %s: %s", newRes.Spec.OmConfigName, err)
		return
	}

	agentKeySecretName, err := c.EnsureAgentKeySecretExists(conn, newRes.Namespace)

	if err != nil {
		log.Error("Failed to generate/get agent key: ", err)
		return
	}

	scaleDown := newRes.Spec.Members < oldRes.Spec.Members

	if scaleDown {
		if err := prepareScaleDownReplicaSet(conn, oldRes, newRes, agentKeySecretName); err != nil {
			log.Error("Failed to prepare OpsManager for scaling down: ", err)
			return
		}
	}

	replicaSetObject := buildReplicaSetStatefulSet(newRes, agentKeySecretName)
	_, err = c.kubeHelper.createOrUpdateStatefulsetsWithService(newRes.Spec.Service, 27017, newRes.Namespace, true, replicaSetObject)
	if err != nil {
		log.Error("Failed to update the StatefulSet: ", err)
		return
	}

	log.Info("Updated statefulset for replicaset")

	if err := c.updateOmDeploymentRs(conn, nil, newRes); err != nil {
		log.Error("Failed to update OpsManager automation config: ", err)
		return
	}

	if scaleDown {
		hostsToRemove := calculateHostsToRemove(oldRes, newRes)
		log.Infow("Stop monitoring removed hosts", "removedHosts", hostsToRemove)
		if err := om.StopMonitoring(conn, hostsToRemove); err != nil {
			log.Errorf("Failed to stop monitoring on hosts %s: %s", hostsToRemove, err)
			return
		}
	}
	log.Info("Updated Replica Set!")
}

func prepareScaleDownReplicaSet(omClient *om.OmConnection, old, new *mongodb.MongoDbReplicaSet, secret string) error {
	log := zap.S().With("replicaSet", new.Name)

	toUpdate := old.Spec.Members - new.Spec.Members
	membersToUpdate := make([]string, toUpdate)
	for i := 0; i < toUpdate; i++ {
		membersToUpdate[i] = GetPodName(old.Name, i+toUpdate)
	}

	// Stage 1. Set Votes and Priority to 0
	deployment, err := omClient.ReadDeployment()
	if err != nil {
		return err
	}

	rs := deployment.GetReplicaSetByName(new.Name)
	for i := new.Spec.Members; i < old.Spec.Members; i++ {
		name := GetPodName(new.Name, i)
		rs.FindMemberByName(name).SetVotes(0).SetPriority(0)
	}
	_, err = omClient.UpdateDeployment(deployment)
	if err != nil {
		log.Debugw("Unable to set votes, priority to 0", "hosts", membersToUpdate)
		return err
	}

	// Wait until agents reach Goal state
	if !om.WaitUntilGoalState(omClient) {
		return errors.New(fmt.Sprintf("Process didn't reach goal state. Setting votes, priority to 0. Hosts: %v", membersToUpdate))
	}

	// Stage 2. Set disabled to true
	deployment, err = omClient.ReadDeployment()
	if err != nil {
		return err
	}
	for i := new.Spec.Members; i < old.Spec.Members; i++ {
		name := GetPodName(new.Name, i)
		deployment.GetProcessByName(name).SetDisabled(true)
	}

	_, err = omClient.UpdateDeployment(deployment)
	if err != nil {
		log.Debugw("Unable to set disabled to true", "hosts", membersToUpdate)
		return err
	}
	// Wait until agents reach Goal state
	if !om.WaitUntilGoalState(omClient) {
		return errors.New(fmt.Sprintf("Process didn't reach Goal state. Setting disabled=true. Hosts: %v", membersToUpdate))
	}

	return nil
}

func (c *MongoDbController) onDeleteReplicaSet(obj interface{}) {
	s := obj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	zap.S().Info("Deleted MongoDbReplicaSet", "replSetName", s.Name)
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func (c *MongoDbController) updateOmDeploymentRs(omConnection *om.OmConnection, old, new *mongodb.MongoDbReplicaSet) error {
	if !waitUntilAllAgentsAreReady(new, omConnection) {
		return errors.New("Some agents failed to register.")
	}

	deployment, err := omConnection.ReadDeployment()
	if err != nil {
		return err
	}

	hostnames, err := c.kubeHelper.GetPodNames(new.Name, new.Namespace, new.Spec.ClusterName)
	if err != nil {
		return err
	}
	members := createStandalonesForReplica(new.Name, new.Spec.Version, hostnames)
	deployment.MergeReplicaSet(new.Name, members)

	deployment.AddMonitoring()

	_, err = omConnection.UpdateDeployment(deployment)
	if err != nil {
		return err
	}

	return nil
}

func validateUpdate(oldSpec, newSpec *mongodb.MongoDbReplicaSet) error {
	if newSpec.Namespace != oldSpec.Namespace {
		return errors.New("Namespaces mismatch")
	}

	if newSpec.Spec.ClusterName != oldSpec.Spec.ClusterName {
		return errors.New("Cluster Names mismatch")
	}

	return nil
}

func waitUntilAllAgentsAreReady(newRes *mongodb.MongoDbReplicaSet, omConnection *om.OmConnection) bool {
	agentHostnames := make([]string, newRes.Spec.Members)
	memberQty := newRes.Spec.Members
	// TODO names of pods must be fetched from Kube api
	serviceName := getOrFormatServiceName(newRes.Spec.Service, newRes.Name)
	for i := 0; i < memberQty; i++ {
		name := GetPodName(newRes.Name, i)
		agentHostnames[i] = fmt.Sprintf("%s.%s", name, serviceName)
	}

	if !om.WaitUntilAgentsHaveRegistered(omConnection, agentHostnames...) {
		return false
	}
	return true
}

func createStandalonesForReplica(name, version string, hostnames []string) []om.Process {
	processes := make([]om.Process, len(hostnames))

	for idx, hostname := range hostnames {
		processes[idx] = om.NewProcess(version).
			SetName(GetPodName(name, idx)).
			SetHostName(hostname)
	}

	return processes
}

func calculateHostsToRemove(old, new *mongodb.MongoDbReplicaSet) []string {
	if new.Spec.Members > old.Spec.Members {
		return make([]string, 0)
	}

	service := getOrFormatServiceName(old.Spec.Service, old.Name)
	qtyToDelete := old.Spec.Members - new.Spec.Members
	result := make([]string, qtyToDelete)
	for i := 0; i < qtyToDelete; i++ {
		result[i] = GetDnsNameFor(new.Name, service, new.Namespace, new.Spec.ClusterName, i+new.Spec.Members)
	}

	return result
}
