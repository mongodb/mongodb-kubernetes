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

	omconfig "com.tengen/cm/config"
	"github.com/10gen/ops-manager-kubernetes/om"
)

func (c *MongoDbController) onAddStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone).DeepCopy()

	// standaloneObject is represented by a StatefulSet in Kubernetes
	standaloneObject := BuildStandalone(s)
	statefulSet, err := c.context.Clientset.AppsV1().StatefulSets(s.Namespace).Create(standaloneObject)
	omConfig := GetOpsManagerConfig()

	fmt.Println("this is what we have in omConfig")
	fmt.Printf("%+v\n", omConfig)

	if err != nil {
		fmt.Println(err)
		return
	}

	agentsOk := false
	for count := 0; count < 3; count++ {
		time.Sleep(3 * time.Second)

		fmt.Println("Will try to get something from the OM API.")
		path := fmt.Sprintf(OpsManagerAgentsResource, omConfig.GroupId)
		agentResponse, err := om.Get(omConfig.BaseUrl, path, omConfig.User, omConfig.PublicApiKey)
		if err != nil {
			fmt.Println("Unable to read from OM API, waiting...")
			fmt.Println(err)

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

	currentDeployment, err := om.ReadDeployment(omConfig.BaseUrl, omConfig.GroupId, omConfig.User, omConfig.PublicApiKey)
	if err != nil {
		fmt.Println("Could not read deployment from OM. Not creating standalone in OM!")
		return
	}
	standaloneOmObject := om.NewStandalone(s.Spec.Version)
	currentDeployment.MergeStandalone(standaloneOmObject)
	_, err = om.ApplyDeployment(omConfig.BaseUrl, omConfig.GroupId, currentDeployment, omConfig.User, omConfig.PublicApiKey)
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
				Spec: BaseContainer(),
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
	// TODO: This is a mock implementation
	omCurrentConfig := testClusterConfiguration()
	updatedAttributes := make([]string, 0)

	processVersion := getProcessVersionForStandalone(new.Name, omCurrentConfig)
	if processVersion == "" {
		return errors.New("Error updating cluster")
	}

	// Check if version has been changed
	if new.Spec.Version != old.Spec.Version {
		if processVersion != old.Spec.Version {
			fmt.Printf("Warning: OM and Kuberentes have different version configured for '%s' process\n", old.Name)
		}

		for _, el := range omCurrentConfig.Processes {
			if el.Name == new.Name {
				updatedAttributes = append(updatedAttributes, "mongodb_version")
				el.Version = processVersion
				break
			}
		}
	}

	if len(updatedAttributes) > 0 {
		// TODO: Update OM with process & new version
		fmt.Printf("Updating Process '%s' with attributes: %v\n", new.Name, updatedAttributes)
	}

	return nil
}

// getProcessVersionForStandalone will traverse the clusterConfig.Processes looking for the
// mongod version of the process we want to update.
func getProcessVersionForStandalone(name string, clusterConfig *omconfig.ClusterConfig) string {
	for _, process := range clusterConfig.Processes {
		if process.Name == name {
			return process.Version
		}
	}

	return ""
}
