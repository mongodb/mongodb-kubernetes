package main

import (
	"fmt"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// NewReplicaSet will return a StatefulSet definition, built on top of Pods.
func NewMongoDbReplicaSet(obj *mongodb.MongoDbReplicaSet) *appsv1.StatefulSet {
	labels := map[string]string{
		"app":   LabelApp,
		"hosts": obj.Spec.HostName,
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      obj.Spec.HostName,
			Namespace: obj.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(obj, schema.GroupVersionKind{
					Group:   mongodb.SchemeGroupVersion.Group,
					Version: mongodb.SchemeGroupVersion.Version,
					Kind:    MongoDbReplicaSet,
				}),
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: obj.Spec.Members,
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

func (c *MongoDbController) onAddReplicaSet(obj interface{}) {
	// s := obj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	fmt.Printf("Not implemented\n")
}

func (c *MongoDbController) onUpdateReplicaSet(oldObj, newObj interface{}) {
	oldRes := oldObj.(*mongodb.MongoDbReplicaSet).DeepCopy()
	newRes := newObj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	fmt.Printf("Updated MongoDbReplicaSet '%s' from %d to %d\n", newRes.Name, *oldRes.Spec.Members, *newRes.Spec.Members)

	if newRes.Namespace != oldRes.Namespace {
		panic("Namespaces mismatch")
	}
	deployment, err := c.context.Clientset.AppsV1().StatefulSets(newRes.Namespace).Update(NewMongoDbReplicaSet(newRes))
	if err != nil {
		fmt.Printf("Error while creating the StatefulSet\n")
		fmt.Println(err)
		return
	}

	fmt.Printf("Updated MongoDbReplicaSet '%s' with %d replicas\n", deployment.Name, *deployment.Spec.Replicas)
}

func (c *MongoDbController) onDeleteReplicaSet(obj interface{}) {
	s := obj.(*mongodb.MongoDbReplicaSet).DeepCopy()

	fmt.Printf("Deleted MongoDbReplicaSet '%s' with Members=%d\n", s.Name, *s.Spec.Members)
}
