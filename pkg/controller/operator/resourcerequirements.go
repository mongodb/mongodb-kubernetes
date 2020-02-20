package operator

import (
	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// buildStorageRequirements returns a corev1.ResourceList definition for storage requirements.
// This is used by the StatefulSet PersistentVolumeClaimTemplate.
func buildStorageRequirements(persistenceConfig *mdbv1.PersistenceConfig, defaultConfig mdbv1.PersistenceConfig) corev1.ResourceList {
	res := corev1.ResourceList{}

	if q := parseQuantityOrZero(mdbv1.GetStorageOrDefault(persistenceConfig, defaultConfig)); !q.IsZero() {
		res[corev1.ResourceStorage] = q
	}

	return res
}

// buildLimitsRequirements returns a corev1.ResourceList definition for limits for CPU and Memory Requirements
// This is used by the StatefulSet containers to allocate resources per Pod.
func buildLimitsRequirements(reqs *mdbv1.PodSpecWrapper) corev1.ResourceList {
	res := corev1.ResourceList{}

	if q := parseQuantityOrZero(reqs.GetCpuOrDefault()); !q.IsZero() {
		res[corev1.ResourceCPU] = q
	}
	if q := parseQuantityOrZero(reqs.GetMemoryOrDefault()); !q.IsZero() {
		res[corev1.ResourceMemory] = q
	}

	return res
}

// buildRequestsRequirements returns a corev1.ResourceList definition for requests for CPU and Memory Requirements
//// This is used by the StatefulSet containers to allocate resources per Pod.
func buildRequestsRequirements(reqs *mdbv1.PodSpecWrapper) corev1.ResourceList {
	res := corev1.ResourceList{}

	if q := parseQuantityOrZero(reqs.GetCpuRequestsOrDefault()); !q.IsZero() {
		res[corev1.ResourceCPU] = q
	}
	if q := parseQuantityOrZero(reqs.GetMemoryRequestsOrDefault()); !q.IsZero() {
		res[corev1.ResourceMemory] = q
	}

	return res
}

// returns
func parseQuantityOrZero(qty string) resource.Quantity {
	q, err := resource.ParseQuantity(qty)
	if err != nil && qty != "" {
		zap.S().Infof("Error converting %s to `resource.Quantity`", qty)
	}

	return q
}
