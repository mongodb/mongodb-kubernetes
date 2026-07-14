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
	// GetKind returns the Kind of the resource. This is needed because
	// when objects are retrieved from the Kubernetes API, the TypeMeta
	// (which contains Kind and APIVersion) is not populated.
	GetKind() string
}
