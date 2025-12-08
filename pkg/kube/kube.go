package kube

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
)

func ObjectKey(namespace, name string) client.ObjectKey {
	return types.NamespacedName{Name: name, Namespace: namespace}
}

func ObjectKeyFromApiObject(obj metav1.Object) client.ObjectKey {
	return ObjectKey(obj.GetNamespace(), obj.GetName())
}

func BaseOwnerReference(owner v1.ObjectOwner) []metav1.OwnerReference {
	if owner == nil {
		return []metav1.OwnerReference{}
	}
	return []metav1.OwnerReference{
		*metav1.NewControllerRef(owner, schema.GroupVersionKind{
			Group:   v1.SchemeGroupVersion.Group,
			Version: v1.SchemeGroupVersion.Version,
			Kind:    owner.GetKind(),
		}),
	}
}
