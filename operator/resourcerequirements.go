package operator

import (
	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1alpha1"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	resource "k8s.io/apimachinery/pkg/api/resource"
)

// buildStorageRequirements returns a corev1.ResourceList definition for storage requirements.
// This is used by the StatefulSet PersistentVolumeClaimTemplate.
func buildStorageRequirements(reqs mongodb.MongoDbRequirements) corev1.ResourceList {
	res := corev1.ResourceList{}
	storageReqs := reqs.Storage
	if storageReqs == "" {
		storageReqs = "16G"
	}

	q, err := resource.ParseQuantity(storageReqs)
	if err == nil {
		res[corev1.ResourceStorage] = q
	} else {
		zap.S().Infof("Error converting %s into `resource.Quantity`", storageReqs)
	}

	return res
}

// buildRequirements returns a corev1.ResourceList definition for CPU and Memory Requirements
// This is used by the StatefulSet containers to allocate resources per Pod.
func buildRequirements(reqs mongodb.MongoDbRequirements) corev1.ResourceList {
	res := corev1.ResourceList{}

	if q := parseQuantityOrZero(reqs.Cpu); !q.IsZero() {
		res[corev1.ResourceCPU] = q
	}
	if q := parseQuantityOrZero(reqs.Memory); !q.IsZero() {
		res[corev1.ResourceMemory] = q
	}

	return res
}

// returns
func parseQuantityOrZero(qty string) resource.Quantity {
	q, err := resource.ParseQuantity(qty)
	if err != nil {
		zap.S().Infof("Error converting %s to `resource.Quantity`", qty)
	}

	return q
}
