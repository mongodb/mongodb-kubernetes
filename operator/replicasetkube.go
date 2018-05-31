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
	conn, err := c.getOmConnection(n.Namespace, spec.Project, spec.Credentials)
	if err != nil {
		return err
	}

	agentKeySecretName, err := c.EnsureAgentKeySecretExists(conn, n.Namespace, log)
	if err != nil {
		return errors.New(fmt.Sprintf("Failed to generate/get agent key: %s", err))
	}

	scaleDown := o != nil && spec.Members < o.Spec.Members

	podVars, err := c.buildPodVars(n.Namespace, n.Spec.Project, n.Spec.Credentials, agentKeySecretName)
	if err != nil {
		return err
	}

	replicaBuilder := c.kubeHelper.NewStatefulSetHelper(n).
		SetService(n.ServiceName()).
		SetReplicas(n.Spec.Members).
		SetPersistence(n.Spec.Persistent).
		SetPodSpec(NewDefaultPodSpecWrapper(n.Spec.PodSpec)).
		SetPodVars(podVars).
		SetExposedExternally(true).
		SetLogger(log)
	replicaSetObject := replicaBuilder.BuildStatefulSet()

	if scaleDown {
		if err := prepareScaleDownReplicaSet(conn, replicaSetObject, o, n, log); err != nil {
			return errors.New(fmt.Sprintf("Failed to prepare Ops Manager for scaling down: %s", err))
		}
	}

	err = replicaBuilder.CreateOrUpdateInKubernetes()
	if err != nil {
		return errors.New(fmt.Sprintf("Failed to create/update the StatefulSet: %s", err))
	}

	log.Info("Updated statefulset for replicaset")

	if err := c.updateOmDeploymentRs(conn, o, n, replicaSetObject, log); err != nil {
		return errors.New(fmt.Sprintf("Failed to update Ops Manager automation config: %s", err))
	}

	return nil
}

func prepareScaleDownReplicaSet(omClient om.OmConnection, statefulSet *appsv1.StatefulSet, old, new *mongodb.MongoDbReplicaSet,
	log *zap.SugaredLogger) error {
	hostNames, podNames := GetDnsForStatefulSetReplicasSpecified(statefulSet, new.Spec.ClusterName, old.Spec.Members)
	podNames = podNames[new.Spec.Members:old.Spec.Members]
	hostNames = hostNames[new.Spec.Members:old.Spec.Members]

	return prepareScaleDown(omClient, map[string][]string{new.Name: podNames}, log)
}

func (c *MongoDbController) onDeleteReplicaSet(obj interface{}) {
	rs := obj.(*mongodb.MongoDbReplicaSet)
	log := zap.S().With("replicaSet", rs.Name)

	conn, err := c.getOmConnection(rs.Namespace, rs.Spec.Project, rs.Spec.Credentials)
	if err != nil {
		return
	}

	deployment, err := conn.ReadDeployment()
	if err != nil {
		log.Errorf("Failed to read deployment: %s", err)
		return
	}

	if err = deployment.RemoveReplicaSetByName(rs.Name); err != nil {
		log.Errorf("Failed to remove replica set from Ops Manager deployment. %s", err)
		return
	}

	_, err = conn.UpdateDeployment(deployment)
	if err != nil {
		log.Errorf("Failed to update replica set in Ops Manager: %s", err)
		return
	}

	hostsToRemove, _ := GetDnsNames(rs.Name, rs.ServiceName(), rs.Namespace, rs.Spec.ClusterName, rs.Spec.Members)
	log.Infow("Stop monitoring removed hosts", "removedHosts", hostsToRemove)
	if err := om.StopMonitoring(conn, hostsToRemove); err != nil {
		log.Errorf("Failed to stop monitoring on hosts %s: %s", hostsToRemove, err)
		return
	}

	log.Info("Removed!")
}

// updateOmDeploymentRs performs OM registration operation for the replicaset. So the changes will be finally propagated
// to automation agents in containers
func (c *MongoDbController) updateOmDeploymentRs(omConnection om.OmConnection, old, new *mongodb.MongoDbReplicaSet,
	set *appsv1.StatefulSet, log *zap.SugaredLogger) error {

	err := waitForRsAgentsToRegister(set, new.Spec.ClusterName, omConnection, log)
	if err != nil {
		return err
	}
	replicaSet := buildReplicaSetFromStatefulSet(set, new.Spec.ClusterName, new.Spec.Version)

	err = omConnection.ReadUpdateDeployment(false,
		func(d om.Deployment) error {
			d.MergeReplicaSet(replicaSet, nil)

			d.AddMonitoringAndBackup(replicaSet.Processes[0].HostName(), log)
			return nil
		},
	)
	if err != nil {
		return err
	}

	if err := deleteHostnamesFromMonitoring(omConnection, getAllHostsRs(set, old), getAllHostsRs(set, old), log); err != nil {
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

func getAllHostsRs(set *appsv1.StatefulSet, rs *mongodb.MongoDbReplicaSet) []string {
	if rs == nil {
		return []string{}
	}
	hostnames, _ := GetDnsForStatefulSetReplicasSpecified(set, rs.Spec.ClusterName, rs.Spec.Members)
	return hostnames
}
