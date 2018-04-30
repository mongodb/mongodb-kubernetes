package operator

import (
	"github.com/10gen/ops-manager-kubernetes/om"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

func (c *MongoDbController) onAddStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone).DeepCopy()

	log := zap.S().With("standalone", s.Name)

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

	// standaloneObject is represented by a StatefulSet in Kubernetes
	standaloneObject := buildStandaloneStatefulSet(s, agentKeySecretName)
	_, err = c.kubeHelper.createOrUpdateStatefulsetsWithService(s.Spec.Service, 27017, s.Namespace, true, standaloneObject)
	if err != nil {
		log.Error("Failed to create statefulset: ", err)
		return
	}

	if err := c.updateOmDeployment(conn, s); err != nil {
		log.Error("Failed to create standalone in OM: ", err)
		return
	}

	log.Info("Created Standalone!")
}

func (c *MongoDbController) onUpdateStandalone(oldObj, newObj interface{}) {
	newRes := newObj.(*mongodb.MongoDbStandalone).DeepCopy()
	log := zap.S().With("standalone", newRes.Name)

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

	standaloneObject := buildStandaloneStatefulSet(newRes, agentKeySecretName)
	_, err = c.kubeHelper.createOrUpdateStatefulsetsWithService(newRes.Spec.Service, 27017, newRes.Namespace, true, standaloneObject)

	if err != nil {
		log.Error("Failed to create/update statefulset: ", newRes.Name)
		return
	}

	if err := c.updateOmDeployment(conn, newRes); err != nil {
		log.Error("Failed to update standalone in OM: ", err)
		return
	}

	log.Info("Updated Standalone!")
}

func (c *MongoDbController) onDeleteStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone).DeepCopy()
	log := zap.S().With("Standalone", s.Name)

	c.kubeHelper.deleteService(s.Name, s.Namespace)

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

	hostsToRemove := hostsToRemove(1, 0, s.Name, s.Namespace, s.Spec.Service, s.Spec.ClusterName)
	log.Infow("Stop monitoring removed hosts", "removedHosts", hostsToRemove)
	if err := om.StopMonitoring(conn, hostsToRemove); err != nil {
		log.Errorf("Failed to stop monitoring on hosts %s: %s", hostsToRemove, err)
		return
	}

	log.Info("Removed!")
}

func (c *MongoDbController) updateOmDeployment(omConnection *om.OmConnection, s *mongodb.MongoDbStandalone) error {
	if !om.WaitUntilAgentsHaveRegistered(omConnection, s.Name) {
		return errors.New("Agents never registered! Not creating standalone in OM!")
	}

	currentDeployment, err := omConnection.ReadDeployment()
	if err != nil {
		return errors.New("Could not read deployment from OM. Not creating standalone in OM!")
	}

	hostnames, err := c.kubeHelper.GetPodNames(s.Name, s.Namespace, s.Spec.ClusterName)
	if err != nil {
		return err
	}
	standaloneOmObject := om.NewMongodProcess(s.Name, s.Spec.Version, hostnames[0])

	currentDeployment.MergeStandalone(standaloneOmObject)
	currentDeployment.AddMonitoringAndBackup()

	_, err = omConnection.UpdateDeployment(currentDeployment)
	if err != nil {
		return errors.New("Error while trying to push another deployment.")
	}
	return nil
}
