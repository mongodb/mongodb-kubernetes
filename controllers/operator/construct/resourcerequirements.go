package construct

import (
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"

	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/api/v1/common"
)

// buildStorageRequirements returns a corev1.ResourceList definition for storage requirements.
// This is used by the StatefulSet PersistentVolumeClaimTemplate.
func buildStorageRequirements(persistenceConfig *common.PersistenceConfig, defaultConfig common.PersistenceConfig) corev1.ResourceList {
	res := corev1.ResourceList{}

	if q := ParseQuantityOrZero(mdbv1.GetStorageOrDefault(persistenceConfig, defaultConfig)); !q.IsZero() {
		res[corev1.ResourceStorage] = q
	}

	return res
}

// buildRequirementsFromPodSpec takes a podSpec, and builds a ResourceRequirements
// taking into consideration the default values of the given podSpec
func buildRequirementsFromPodSpec(podSpec mdbv1.PodSpecWrapper) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Limits:   buildLimitsRequirements(&podSpec),
		Requests: buildRequestsRequirements(&podSpec),
	}
}

// buildLimitsRequirements returns a corev1.ResourceList definition for limits for CPU and Memory Requirements
// This is used by the StatefulSet containers to allocate resources per Pod.
func buildLimitsRequirements(reqs *mdbv1.PodSpecWrapper) corev1.ResourceList {
	res := corev1.ResourceList{}

	if q := ParseQuantityOrZero(reqs.GetCpuOrDefault()); !q.IsZero() {
		res[corev1.ResourceCPU] = q
	}
	if q := ParseQuantityOrZero(reqs.GetMemoryOrDefault()); !q.IsZero() {
		res[corev1.ResourceMemory] = q
	}

	return res
}

// buildRequestsRequirements returns a corev1.ResourceList definition for requests for CPU and Memory Requirements
// This is used by the StatefulSet containers to allocate resources per Pod.
func buildRequestsRequirements(reqs *mdbv1.PodSpecWrapper) corev1.ResourceList {
	res := corev1.ResourceList{}

	if q := ParseQuantityOrZero(reqs.GetCpuRequestsOrDefault()); !q.IsZero() {
		res[corev1.ResourceCPU] = q
	}
	if q := ParseQuantityOrZero(reqs.GetMemoryRequestsOrDefault()); !q.IsZero() {
		res[corev1.ResourceMemory] = q
	}

	return res
}

// TODO: this function needs to be unexported - refactor tests and make this private
func ParseQuantityOrZero(qty string) resource.Quantity {
	q, err := resource.ParseQuantity(qty)
	if err != nil && qty != "" {
		zap.S().Infof("Error converting %s to `resource.Quantity`", qty)
	}

	return q
}
