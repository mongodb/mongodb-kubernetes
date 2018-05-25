package operator

import (
	"errors"
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/om"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1beta1"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
)

func (c *MongoDbController) onAddReplicaSet(obj interface{}) {
	s := obj.(*mongodb.MongoDbReplicaSet)

	log := zap.S().With("replica set", s.Name)

	log.Infow("Creating Replica set", "config", s.Spec)

	if err := c.doRsProcessing(nil, s, log); err != nil {
		log.Error(err)
		return
	}

	log.Info("Created Replica Set!")
}

func (c *MongoDbController) onUpdateReplicaSet(oldObj, newObj interface{}) {
	o := oldObj.(*mongodb.MongoDbReplicaSet)
	n := newObj.(*mongodb.MongoDbReplicaSet)
	log := zap.S().With("replica set", n.Name)

	log.Infow("Updating MongoDbReplicaSet", "oldConfig", o.Spec, "newConfig", n.Spec)

	if err := validateReplicaSetUpdate(o, n); err != nil {
		log.Error(err)
		return
	}

	if err := c.doRsProcessing(o, n, log); err != nil {
		log.Error(err)
		return
	}

	log.Info("Updated Replica Set!")
}

func (c *MongoDbController) doRsProcessing(o, n *mongodb.MongoDbReplicaSet, log *zap.SugaredLogger) error {
	spec := n.Spec
	conn, err := c.getOmConnection(n.Namespace, spec.OmConfigName)
	if err != nil {
		return errors.New(fmt.Sprintf("Failed to read Ops Manager config map %s: %s", spec.OmConfigName, err))
	}

	agentKeySecretName, err := c.EnsureAgentKeySecretExists(conn, n.Namespace, log)
	if err != nil {
		return errors.New(fmt.Sprintf("Failed to generate/get agent key: %s", err))
	}

	scaleDown := o != nil && spec.Members < o.Spec.Members

	if scaleDown {
		if err := prepareScaleDownReplicaSet(conn, o, n, log); err != nil {
			return errors.New(fmt.Sprintf("Failed to prepare Ops Manager for scaling down: %s", err))
		}
	}

	replicaSetObject := buildStatefulSet(n, n.Name, n.ServiceName(), n.Namespace, spec.OmConfigName, agentKeySecretName,
		spec.Members, spec.Persistent, mongodb.PodSpecWrapper{spec.PodSpec, NewDefaultPodSpec()})
	_, err = c.kubeHelper.createOrUpdateStatefulsetsWithService(n, MongoDbDefaultPort, n.Namespace, true, log, replicaSetObject)
	if err != nil {
		return errors.New(fmt.Sprintf("Failed to create/update the StatefulSet: %s", err))
	}

	log.Info("Updated statefulset for replicaset")

	if err := c.updateOmDeploymentRs(conn, nil, n, replicaSetObject, log); err != nil {
		return errors.New(fmt.Sprintf("Failed to update Ops Manager automation config: %s", err))
	}

	if scaleDown {
		hostsToRemove := calculateHostsToRemove(o, n, replicaSetObject)
		log.Infow("Stop monitoring removed hosts", "removedHosts", hostsToRemove)
		if err := om.StopMonitoring(conn, hostsToRemove); err != nil {
			return errors.New(fmt.Sprintf("Failed to stop monitoring on hosts %s: %s", hostsToRemove, err))
		}
	}
	return nil
}

func prepareScaleDownReplicaSet(omClient *om.OmConnection, old, new *mongodb.MongoDbReplicaSet, log *zap.SugaredLogger) error {
	membersToUpdate := make([]string, 0)
	for i := new.Spec.Members; i < old.Spec.Members; i++ {
		membersToUpdate = append(membersToUpdate, GetPodName(old.Name, i))
	}

	// Stage 1. Set Votes and Priority to 0
	deployment, err := omClient.ReadDeployment()
	if err != nil {
		return err
	}

	for _, el := range membersToUpdate {
		deployment.MarkRsMemberUnvoted(new.Name, el)
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

	for _, el := range membersToUpdate {
		deployment.DisableProcess(el)
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
	rs := obj.(*mongodb.MongoDbReplicaSet)
	log := zap.S().With("replicaSet", rs.Name)

	conn, err := c.getOmConnection(rs.Namespace, rs.Spec.OmConfigName)
	if err != nil {
		log.Errorf("Failed to read OpsManager config map %s: %s", rs.Spec.OmConfigName, err)
		return
	}

	deployment, err := conn.ReadDeployment()
	if err != nil {
		log.Errorf("Failed to read deployment: %s", err)
		return
	}

	if err = deployment.RemoveReplicaSetByName(rs.Name); err != nil {
		log.Errorf("Failed to remove replica set. %s", err)
		return
	}

	_, err = conn.UpdateDeployment(deployment)
	if err != nil {
		log.Errorf("Failed to update replica set: %s", err)
		return
	}

	rsStatefulSet, err := c.kubeHelper.readStatefulSet(rs.Namespace, rs.Name)

	if err != nil {
		log.Errorf("Failed to read stateful set %s: %s", rs.Name, err)
		return
	}

	hostsToRemove, _ := GetDnsForStatefulSet(rsStatefulSet, rs.Spec.ClusterName)
	log.Infow("Stop monitoring removed hosts", "removedHosts", hostsToRemove)
	if err := om.StopMonitoring(conn, hostsToRemove); err != nil {
		log.Errorf("Failed to stop monitoring on hosts %s: %s", hostsToRemove, err)
		return
	}

	log.Info("Removed!")
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func (c *MongoDbController) updateOmDeploymentRs(omConnection *om.OmConnection, old, new *mongodb.MongoDbReplicaSet,
	set *appsv1.StatefulSet, log *zap.SugaredLogger) error {

	err := waitForRsAgentsToRegister(set, new.Spec.ClusterName, omConnection, log)
	if err != nil {
		return err
	}

	deployment, err := omConnection.ReadDeployment()
	if err != nil {
		return err
	}

	replicaSet := buildReplicaSetFromStatefulSet(set, new.Spec.ClusterName, new.Spec.Version)
	deployment.MergeReplicaSet(replicaSet, nil)

	deployment.AddMonitoringAndBackup(replicaSet.Processes[0].HostName(), log)

	_, err = omConnection.UpdateDeployment(deployment)
	if err != nil {
		return err
	}

	return nil
}

func validateReplicaSetUpdate(oldSpec, newSpec *mongodb.MongoDbReplicaSet) error {
	if newSpec.Namespace != oldSpec.Namespace {
		return errors.New("Namespace cannot change for existing object")
	}

	if newSpec.Spec.ClusterName != oldSpec.Spec.ClusterName {
		return errors.New("Cluster name cannot change for existing object")
	}

	return nil
}

func calculateHostsToRemove(old, new *mongodb.MongoDbReplicaSet, set *appsv1.StatefulSet) []string {
	hostnames, _ := GetDnsForStatefulSet(set, new.Spec.ClusterName)
	result := make([]string, 0)
	for i := new.Spec.Members; i < old.Spec.Members; i++ {
		result = append(result, hostnames[i])
	}
	return result

}
