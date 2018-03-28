package operator

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/om"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *MongoDbController) onAddStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone).DeepCopy()

	agentKeySecretName, err := c.EnsureAgentKeySecretExists(s.Namespace, NewOpsManagerConnectionFromEnv())

	if err != nil {
		fmt.Println("Failed to generate/get agent key")
		fmt.Println(err)
		return
	}

	// standaloneObject is represented by a StatefulSet in Kubernetes
	standaloneObject := buildStandaloneStatefulSet(s, agentKeySecretName)

	// TODO we need to query for statefulset first in case previous create process failed on OM communication and
	// statefulset was indeed created to make process idempotent
	_, err = c.kubeHelper.createOrUpdateStatefulsetsWithService(s.Spec.Service, 27017, s.Namespace, true, standaloneObject)
	if err != nil {
		fmt.Printf("Error. Failed to create statefulset '%s'\n", s.Name)
		fmt.Println(err)
		return
	}

	if !updateOmDeployment(s) {
		fmt.Println("Failed to create standalone in OM")
		return
	}

	fmt.Printf("Created Standalone: '%s'\n", s.Name)
}

func (c *MongoDbController) onUpdateStandalone(oldObj, newObj interface{}) {
	newRes := newObj.(*mongodb.MongoDbStandalone).DeepCopy()

	agentKeySecretName, err := c.EnsureAgentKeySecretExists(newRes.Namespace, NewOpsManagerConnectionFromEnv())

	if err != nil {
		fmt.Println("Failed to generate/get agent key")
		fmt.Println(err)
		return
	}

	standaloneObject := buildStandaloneStatefulSet(newRes, agentKeySecretName)
	_, err = c.kubeHelper.createOrUpdateStatefulsetsWithService(newRes.Spec.Service, 27017, newRes.Namespace, true, standaloneObject)

	if err != nil {
		fmt.Printf("Error. Failed to create/update statefulset '%s'\n", newRes.Name)
		fmt.Println(err)
	}

	if !updateOmDeployment(newRes) {
		fmt.Println("Failed to update standalone in OM")
		return
	}

	fmt.Printf("Updated Standalone: '%s'\n", newRes.Name)
}

func (c *MongoDbController) onDeleteStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone).DeepCopy()

	deleteOptions := metav1.NewDeleteOptions(0)
	c.context.Clientset.AppsV1().StatefulSets(s.Namespace).Delete(s.Name, deleteOptions)
	fmt.Printf("Deleted MongoDbStandalone '%s'\n", s.Name)
}

func updateOmDeployment(s *mongodb.MongoDbStandalone) bool {
	omConnection := NewOpsManagerConnectionFromEnv()

	if !om.WaitUntilAgentsHaveRegistered(omConnection, s.Name) {
		fmt.Println("Agents never registered! Not creating standalone in OM!")
		return false
	}

	currentDeployment, err := omConnection.ReadDeployment()
	if err != nil {
		fmt.Println("Could not read deployment from OM. Not creating standalone in OM!")
		return false
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
		fmt.Println("Error while trying to push another deployment.")
		fmt.Println(err)
		return false
	}
	return true
}
