package v1

import "sigs.k8s.io/controller-runtime/pkg/client"

// +kubebuilder:object:generate=false
type ResourceOwner interface {
	GetName() string
	GetNamespace() string
	ObjectKey() client.ObjectKey
	GetOwnerLabels() map[string]string
}

// +kubebuilder:object:generate=false
type ObjectOwner interface {
	ResourceOwner
	client.Object
}
