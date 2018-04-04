package operator

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/om"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *MongoDbController) onAddStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone).DeepCopy()

	log := zap.S().With("standalone", s.Name)

	agentKeySecretName, err := c.EnsureAgentKeySecretExists(s.Namespace, NewOpsManagerConnectionFromEnv())

	if err != nil {
		log.Error("Failed to generate/get agent key: ", err)
		return
	}

	// standaloneObject is represented by a StatefulSet in Kubernetes
	standaloneObject := buildStandaloneStatefulSet(s, agentKeySecretName)

	// TODO we need to query for statefulset first in case previous create process failed on OM communication and
	// statefulset was indeed created to make process idempotent
	_, err = c.kubeHelper.createOrUpdateStatefulsetsWithService(s.Spec.Service, 27017, s.Namespace, true, standaloneObject)
	if err != nil {
		log.Error("Failed to create statefulset: ", err)
		return
	}

	if err := updateOmDeployment(s); err != nil {
		log.Error("Failed to create standalone in OM: ", err)
		return
	}

	log.Info("Created Standalone!")
}

func (c *MongoDbController) onUpdateStandalone(oldObj, newObj interface{}) {
	newRes := newObj.(*mongodb.MongoDbStandalone).DeepCopy()
	log := zap.S().With("standalone", newRes.Name)

	agentKeySecretName, err := c.EnsureAgentKeySecretExists(newRes.Namespace, NewOpsManagerConnectionFromEnv())

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

	if err := updateOmDeployment(newRes); err != nil {
		log.Error("Failed to update standalone in OM: ", err)
		return
	}

	log.Info("Updated Standalone!")
}

func (c *MongoDbController) onDeleteStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone).DeepCopy()

	deleteOptions := metav1.NewDeleteOptions(0)
	c.context.Clientset.AppsV1().StatefulSets(s.Namespace).Delete(s.Name, deleteOptions)
	zap.S().Info("Deleted MongoDbStandalone ", s.Name)
}

func updateOmDeployment(s *mongodb.MongoDbStandalone) error {
	omConnection := NewOpsManagerConnectionFromEnv()

	if !om.WaitUntilAgentsHaveRegistered(omConnection, s.Name) {
		return errors.New("Agents never registered! Not creating standalone in OM!")
	}

	currentDeployment, err := omConnection.ReadDeployment()
	if err != nil {
		return errors.New("Could not read deployment from OM. Not creating standalone in OM!")
	}

	// TODO fix hostnames in CLOUDP-28316
	serviceName := getOrFormatServiceName(s.Spec.Service, s.Name)
	hostname := fmt.Sprintf("%s-0.%s.default.svc.cluster.local", s.Name, serviceName)
	standaloneOmObject := om.NewProcess(s.Spec.Version).
		SetName(s.Name).
		SetHostName(hostname)

	currentDeployment.MergeStandalone(standaloneOmObject)

	_, err = omConnection.UpdateDeployment(currentDeployment)
	if err != nil {
		return errors.New("Error while trying to push another deployment.")
	}
	return nil
}
