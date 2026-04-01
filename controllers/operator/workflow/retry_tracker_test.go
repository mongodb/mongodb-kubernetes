package workflow

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newTestObject(annotations map[string]string) client.Object {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test",
			Namespace:   "default",
			Annotations: annotations,
		},
	}
	return cm
}

func TestGetOMRetryCount_NilAnnotations(t *testing.T) {
	obj := newTestObject(nil)
	if got := GetOMRetryCount(obj); got != 0 {
		t.Errorf("GetOMRetryCount(nil annotations) = %d, want 0", got)
	}
}

func TestGetOMRetryCount_MissingKey(t *testing.T) {
	obj := newTestObject(map[string]string{"other": "value"})
	if got := GetOMRetryCount(obj); got != 0 {
		t.Errorf("GetOMRetryCount(missing key) = %d, want 0", got)
	}
}

func TestGetOMRetryCount_InvalidValue(t *testing.T) {
	obj := newTestObject(map[string]string{OMRetryCountAnnotation: "not-a-number"})
	if got := GetOMRetryCount(obj); got != 0 {
		t.Errorf("GetOMRetryCount(invalid) = %d, want 0", got)
	}
}

func TestGetOMRetryCount_ValidValue(t *testing.T) {
	obj := newTestObject(map[string]string{OMRetryCountAnnotation: "5"})
	if got := GetOMRetryCount(obj); got != 5 {
		t.Errorf("GetOMRetryCount(5) = %d, want 5", got)
	}
}

func TestIncrementOMRetryCount_FromZero(t *testing.T) {
	obj := newTestObject(nil)
	IncrementOMRetryCount(obj)
	if got := GetOMRetryCount(obj); got != 1 {
		t.Errorf("after IncrementOMRetryCount from 0, got %d, want 1", got)
	}
}

func TestIncrementOMRetryCount_FromExisting(t *testing.T) {
	obj := newTestObject(map[string]string{OMRetryCountAnnotation: "3"})
	IncrementOMRetryCount(obj)
	if got := GetOMRetryCount(obj); got != 4 {
		t.Errorf("after IncrementOMRetryCount from 3, got %d, want 4", got)
	}
}

func TestResetOMRetryCount(t *testing.T) {
	obj := newTestObject(map[string]string{OMRetryCountAnnotation: "5"})
	ResetOMRetryCount(obj)
	if got := GetOMRetryCount(obj); got != 0 {
		t.Errorf("after ResetOMRetryCount, got %d, want 0", got)
	}
	// Verify the annotation key is actually removed
	if _, exists := obj.GetAnnotations()[OMRetryCountAnnotation]; exists {
		t.Error("ResetOMRetryCount should remove the annotation key")
	}
}

func TestResetOMRetryCount_NilAnnotations(t *testing.T) {
	obj := newTestObject(nil)
	// Should not panic
	ResetOMRetryCount(obj)
	if got := GetOMRetryCount(obj); got != 0 {
		t.Errorf("after ResetOMRetryCount(nil), got %d, want 0", got)
	}
}
