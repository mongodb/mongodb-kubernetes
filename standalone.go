package main

import (
	"errors"
	"fmt"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	omconfig "com.tengen/cm/config"
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

	// wait until the pods are ready and then contact OM to create the new object
	// om.CreateStandalone(standaloneObject)

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
			Name:      obj.Spec.HostName,
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
