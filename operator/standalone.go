package operator

import (
	"fmt"
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"github.com/10gen/ops-manager-kubernetes/om"
)

func (c *MongoDbController) onAddStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone).DeepCopy()

	// standaloneObject is represented by a StatefulSet in Kubernetes
	standaloneObject := buildStandalone(s)

	// TODO we need to query for statefulset first in case previous create process failed on OM communication and
	// statefulset was indeed created to make process idempotent
	statefulSet, err := c.StatefulSetApi(s.Namespace).Create(standaloneObject)
	if err != nil {
		fmt.Printf("Error. Failed to create object '%s'\n", statefulSet.ObjectMeta.Name)
		fmt.Println(err)
		return
	}

	if !updateOmDeployment(s) {
		fmt.Println("Failed to create standalone in OM")
		return
	}

	fmt.Printf("Created Standalone: '%s'\n", statefulSet.ObjectMeta.Name)
}

func (c *MongoDbController) onUpdateStandalone(oldObj, newObj interface{}) {
	newRes := newObj.(*mongodb.MongoDbStandalone).DeepCopy()

	standaloneObject := buildStandalone(newRes)
	statefulSet, err := c.StatefulSetApi(newRes.Namespace).Update(standaloneObject)

	if err != nil {
		fmt.Printf("Error. Failed to update object '%s'\n", statefulSet.ObjectMeta.Name)
		fmt.Println(err)
	}

	if !updateOmDeployment(newRes) {
		fmt.Println("Failed to update standalone in OM")
		return
	}

	fmt.Printf("Updated Standalone: '%s'\n", statefulSet.ObjectMeta.Name)
}

func (c *MongoDbController) onDeleteStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone).DeepCopy()

	deleteOptions := metav1.NewDeleteOptions(0)
	c.context.Clientset.AppsV1().StatefulSets(s.Namespace).Delete(s.Name, deleteOptions)
	fmt.Printf("Deleted MongoDbStandalone '%s'\n", s.Name)
}

func updateOmDeployment(s *mongodb.MongoDbStandalone) bool {
	omConnection := NewOpsManagerConnectionFromEnv()

	if !om.WaitUntilAgentsHaveRegistered(omConnection, s.Spec.HostnamePrefix) {
		fmt.Println("Agents never registered! Not creating standalone in OM!")
		return false
	}

	currentDeployment, err := omConnection.ReadDeployment()
	if err != nil {
		fmt.Println("Could not read deployment from OM. Not creating standalone in OM!")
		return false
	}

	hostname := fmt.Sprintf("%s-0", s.Spec.HostnamePrefix)
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
