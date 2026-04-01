package workflow

import (
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// OMRetryCountAnnotation tracks consecutive OM API failure count on a resource.
	OMRetryCountAnnotation = "mongodb.com/v1.omRetryCount"
)

// GetOMRetryCount reads the OM retry count from the resource annotations.
// Returns 0 when the annotation is missing or unparseable.
func GetOMRetryCount(obj client.Object) int {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return 0
	}
	v, ok := annotations[OMRetryCountAnnotation]
	if !ok {
		return 0
	}
	count, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return count
}

// IncrementOMRetryCount increments the OM retry count annotation on the resource.
func IncrementOMRetryCount(obj client.Object) {
	count := GetOMRetryCount(obj)
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[OMRetryCountAnnotation] = strconv.Itoa(count + 1)
	obj.SetAnnotations(annotations)
}

// ResetOMRetryCount removes the OM retry count annotation from the resource.
func ResetOMRetryCount(obj client.Object) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return
	}
	delete(annotations, OMRetryCountAnnotation)
	obj.SetAnnotations(annotations)
}
