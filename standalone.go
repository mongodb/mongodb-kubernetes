package main

import (
	"errors"
	"fmt"
	"time"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/10gen/ops-manager-kubernetes/om"
)

func (c *MongoDbController) onAddStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone).DeepCopy()

	// standaloneObject is represented by a StatefulSet in Kubernetes
	standaloneObject := BuildStandalone(s)
	statefulSet, err := c.context.Clientset.AppsV1().StatefulSets(s.Namespace).Create(standaloneObject)
	if err != nil {
		fmt.Println(err)
		return
	}

	omConfig := GetOpsManagerConfig()
	omConnection := om.NewOpsManagerConnection(omConfig.BaseUrl, omConfig.GroupId, omConfig.User, omConfig.PublicApiKey)

	agentsOk := false
	// TODO: exponential backoff
	for count := 0; count < 3; count++ {
		time.Sleep(3 * time.Second)

		fmt.Println("Will try to get something from the OM API.")
		path := fmt.Sprintf(OpsManagerAgentsResource, omConfig.GroupId)
		agentResponse, err := omConnection.Get(path)
		if err != nil {
			fmt.Println("Unable to read from OM API, waiting...")
			fmt.Println(err)
			continue
		}

		fmt.Println("Checking if the agent have registered yet")
		fmt.Printf("Checked %s against response\n", s.Spec.HostnamePrefix)
		if CheckAgentExists(s.Spec.HostnamePrefix, agentResponse) {
			fmt.Println("Found agents have already registered!")
			agentsOk = true
			break
		}
		fmt.Println("Agents have not registered with OM, waiting...")
	}
	if !agentsOk {
		fmt.Println("Agents never registered! not creating standalone in OM!")
		return
	}

	 currentDeployment, err := omConnection.ReadDeployment()
	 if err != nil {
	 	fmt.Println("Could not read deployment from OM. Not creating standalone in OM!")
	 	return
	 }

	hostname := fmt.Sprintf("%s-0", s.Spec.HostnamePrefix)
	standaloneOmObject := om.NewProcess(s.Spec.Version).
		SetName(s.Name).
		SetHostName(hostname)

	currentDeployment.MergeStandalone(standaloneOmObject)

	_, err = omConnection.ApplyDeployment(currentDeployment)
	if err != nil {
		fmt.Println("Error while trying to push another deployment.")
		fmt.Println(err)
	}

	fmt.Printf("Created Standalone: '%s'\n", statefulSet.ObjectMeta.Name)
}

func (c *MongoDbController) onUpdateStandalone(oldObj, newObj interface{}) {
	oldRes := oldObj.(*mongodb.MongoDbStandalone).DeepCopy()
	newRes := newObj.(*mongodb.MongoDbStandalone).DeepCopy()

	standaloneObject := BuildStandalone(newRes)
	statefulSet, err := c.context.Clientset.AppsV1().StatefulSets(newRes.Namespace).Update(standaloneObject)

	if err != nil {
		fmt.Printf("Error. Could not update object '%s'\n", statefulSet.ObjectMeta.Name)
		fmt.Println(err)
	}

	// wait until pods are ready (they have been restarted and registered into OM)
	// get differences and update OM with differences
	// om.UpdateStandalone(newRes.Name, diff)
	err = UpdateStandalone(newRes, oldRes)
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Printf("Updated Standalone\n")
}

func (c *MongoDbController) onDeleteStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone).DeepCopy()

	deleteOptions := metav1.NewDeleteOptions(0)
	c.context.Clientset.AppsV1().StatefulSets(s.Namespace).Delete(s.Name, deleteOptions)
	fmt.Printf("Deleted MongoDbStandalone '%s'\n", s.Name)
}

// BuildStandalone returns a StatefulSet which is how MongoDB Standalone objects
// are mapped into Kubernetes objects.
func BuildStandalone(obj *mongodb.MongoDbStandalone) *appsv1.StatefulSet {
	labels := map[string]string{
		"app":        LabelApp,
		"controller": LabelController,
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      obj.Spec.HostnamePrefix,
			Namespace: obj.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(obj, schema.GroupVersionKind{
					Group:   mongodb.SchemeGroupVersion.Group,
					Version: mongodb.SchemeGroupVersion.Version,
					Kind:    MongoDbStandalone,
				}),
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: MakeIntReference(StandaloneMembers),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: BaseContainer(obj.Name),
			},
		},
	}
}

// UpdateStandalone in OM an object that was updated. It needs to check which updated
// parameters will have to be notified to OM (so they get also updated).
// Supported update parameters:
// + mongodb version
// This is a very imperative kind of programming in line with go principles.
func UpdateStandalone(new, old *mongodb.MongoDbStandalone) error {
	omConfig := GetOpsManagerConfig()
	omConnection := om.NewOpsManagerConnection(omConfig.BaseUrl, omConfig.GroupId, omConfig.User, omConfig.PublicApiKey)
	currentDeployment, err := omConnection.ReadDeployment()
	if err != nil {
		return err
	}

	// TODO change the statefulset for the standalone

	hostname := fmt.Sprintf("%s-0", new.Spec.HostnamePrefix)
	standaloneOmObject := om.NewProcess(new.Spec.Version).
		SetName(new.Name).
		SetHostName(hostname)

	currentDeployment.MergeStandalone(standaloneOmObject)

	omConnection.ApplyDeployment(currentDeployment)

	return nil
}
