package v1

import (
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
)

// CustomResourceReadWriter is an interface for all Custom Resources with Status read/write capabilities
// TODO this may be a good candidate for further refactoring
// +kubebuilder:object:generate=false
type CustomResourceReadWriter interface {
	client.Object
	status.Reader
	status.Writer
}
