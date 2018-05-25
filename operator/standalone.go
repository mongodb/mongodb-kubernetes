package operator

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/om"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1beta1"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
)

func (c *MongoDbController) onAddStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone)

	log := zap.S().With("standalone", s.Name)

	log.Infow("Creating MongoDbStandalone", "config", s.Spec)

	if err := c.doStandaloneProcessing(nil, s, log); err != nil {
		log.Error(err)
		return
	}

	log.Info("Created!")
}

func (c *MongoDbController) onUpdateStandalone(oldObj, newObj interface{}) {
	o := newObj.(*mongodb.MongoDbStandalone)
	n := newObj.(*mongodb.MongoDbStandalone)
	log := zap.S().With("standalone", n.Name)

	log.Infow("Updating MongoDbStandalone", "oldConfig", o.Spec, "newConfig", n.Spec)

	if err := validateUpdateStandalone(o, n); err != nil {
		log.Error(err)
		return
	}

	if err := c.doStandaloneProcessing(o, n, log); err != nil {
		log.Error(err)
		return
	}

	log.Info("Updated!")
}

func (c *MongoDbController) onDeleteStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone)
	log := zap.S().With("Standalone", s.Name)

	conn, err := c.getOmConnection(s.Namespace, s.Spec.OmConfigName)
	if err != nil {
		log.Errorf("Failed to read OpsManager config map %s: %s", s.Spec.OmConfigName, err)
		return
	}

	deployment, err := conn.ReadDeployment()
	if err != nil {
		log.Errorf("Failed to read deployment: %s", err)
		return
	}

	if err = deployment.RemoveProcessByName(s.Name); err != nil {
		log.Error(err)
	}

	_, err = conn.UpdateDeployment(deployment)
	if err != nil {
		log.Errorf("Failed to update Standalone: %s", err)
		return
	}

	rsStatefulSet, err := c.kubeHelper.readStatefulSet(s.Namespace, s.Name)

	if err != nil {
		log.Errorf("Failed to read stateful set %s: %s", s.Name, err)
		return
	}

	hostsToRemove, _ := GetDnsForStatefulSet(rsStatefulSet, s.Spec.ClusterName)

	log.Infow("Stop monitoring removed hosts", "removedHosts", hostsToRemove)
	if err := om.StopMonitoring(conn, hostsToRemove); err != nil {
		log.Errorf("Failed to stop monitoring on hosts %s: %s", hostsToRemove, err)
		return
	}

	log.Info("Removed!")
}

func (c *MongoDbController) doStandaloneProcessing(o, n *mongodb.MongoDbStandalone, log *zap.SugaredLogger) error {
	spec := n.Spec
	conn, err := c.getOmConnection(n.Namespace, spec.OmConfigName)
	if err != nil {
		return errors.New(fmt.Sprintf("Failed to read OpsManager config map %s: %s", spec.OmConfigName, err))
	}

	agentKeySecretName, err := c.EnsureAgentKeySecretExists(conn, n.Namespace, log)
	if err != nil {
		return errors.New(fmt.Sprintf("Failed to generate/get agent key: %s", err))
	}

	// standaloneSet is represented by a StatefulSet in Kubernetes
	podSpec := mongodb.PodSpecWrapper{mongodb.MongoDbPodSpec{MongoDbPodSpecStandalone: spec.PodSpec}, NewDefaultPodSpec()}
	standaloneSet := buildStatefulSet(n, n.Name, n.ServiceName(), n.Namespace, spec.OmConfigName, agentKeySecretName,
		1, spec.Persistent, podSpec)
	_, err = c.kubeHelper.createOrUpdateStatefulsetsWithService(n, MongoDbDefaultPort, n.Namespace, true, log, standaloneSet)
	if err != nil {
		return errors.New(fmt.Sprintf("Failed to create statefulset: %s", err))
	}

	if err := c.updateOmDeployment(conn, n, standaloneSet, log); err != nil {
		return errors.New(fmt.Sprintf("Failed to create standalone in OM: %s", err))
	}
	return nil
}

func (c *MongoDbController) updateOmDeployment(omConnection *om.OmConnection, s *mongodb.MongoDbStandalone,
	set *appsv1.StatefulSet, log *zap.SugaredLogger) error {
	if err := waitForRsAgentsToRegister(set, s.Spec.ClusterName, omConnection, log); err != nil {
		return err
	}

	currentDeployment, err := omConnection.ReadDeployment()
	if err != nil {
		return errors.New("Could not read deployment from OM. Not creating standalone in OM!")
	}

	standaloneOmObject := createProcesses(set, s.Spec.ClusterName, s.Spec.Version, om.ProcessTypeMongod)

	currentDeployment.MergeStandalone(standaloneOmObject[0], nil)
	currentDeployment.AddMonitoringAndBackup(standaloneOmObject[0].HostName(), log)

	_, err = omConnection.UpdateDeployment(currentDeployment)
	if err != nil {
		return errors.New("Error while trying to push another deployment.")
	}
	return nil
}

func validateUpdateStandalone(oldSpec, newSpec *mongodb.MongoDbStandalone) error {
	if newSpec.Namespace != oldSpec.Namespace {
		return errors.New("Namespace cannot change for existing object")
	}

	if newSpec.Spec.ClusterName != oldSpec.Spec.ClusterName {
		return errors.New("Cluster Name cannot change for existing object")
	}

	return nil
}
