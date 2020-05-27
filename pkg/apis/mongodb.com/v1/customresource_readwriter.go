package v1

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// CustomResourceReadWriter is an interface for all Custom Resources with Status read/write capabilities
// TODO this may be a good candidate for further refactoring
type CustomResourceReadWriter interface {
	runtime.Object
	metav1.Object
	status.Reader
	status.Writer
}
