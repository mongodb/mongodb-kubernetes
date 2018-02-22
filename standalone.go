package main

import (
	"fmt"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func NewStandalone(obj *mongodb.MongoDbStandalone) *appsv1.StatefulSet {
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

func (c *MongoDbController) onAddStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone).DeepCopy()

	standaloneObject := NewStandalone(s)
	statefulSet, err := c.context.Clientset.AppsV1().StatefulSets(s.Namespace).Create(standaloneObject)

	if err != nil {
		fmt.Printf("Error while creating StatefulSet\n")
		fmt.Println(err)
		return
	}

	// wait until the pods are ready and then contact OM to create the new object

	fmt.Printf("Created Standalone: '%s'\n", statefulSet.ObjectMeta.Name)
}

func (c *MongoDbController) onUpdateStandalone(oldObj, newObj interface{}) {
	oldRes := oldObj.(*mongodb.MongoDbStandalone).DeepCopy()
	newRes := newObj.(*mongodb.MongoDbStandalone).DeepCopy()

	if newRes.Namespace != oldRes.Namespace {
		panic("Two different namespaces?? whaaat?")
	}
	fmt.Printf("Updated Standalone\n")
}

func (c *MongoDbController) onDeleteStandalone(obj interface{}) {
	s := obj.(*mongodb.MongoDbStandalone).DeepCopy()

	fmt.Printf("Deleted MongoDbStandalone '%s'\n", s.Name)
}
