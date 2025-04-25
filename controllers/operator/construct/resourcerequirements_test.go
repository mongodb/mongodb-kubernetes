package construct

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

func TestStorageRequirements(t *testing.T) {
	// value is provided - the default is ignored
	podSpec := mdbv1.NewEmptyPodSpecWrapperBuilder().
		SetSinglePersistence(mdbv1.NewPersistenceBuilder("40G")).
		SetDefault(mdbv1.NewPodSpecWrapperBuilder().SetSinglePersistence(mdbv1.NewPersistenceBuilder("12G"))).
		Build()

	req := buildStorageRequirements(podSpec.Persistence.SingleConfig, *podSpec.Default.Persistence.SingleConfig)

	assert.Len(t, req, 1)
	quantity := req[corev1.ResourceStorage]
	assert.Equal(t, int64(40000000000), (&quantity).Value())

	// value is not provided - the default is used
	podSpec = mdbv1.NewEmptyPodSpecWrapperBuilder().
		SetDefault(mdbv1.NewPodSpecWrapperBuilder().SetSinglePersistence(mdbv1.NewPersistenceBuilder("5G"))).
		Build()

	req = buildStorageRequirements(podSpec.Persistence.SingleConfig, *podSpec.Default.Persistence.SingleConfig)

	assert.Len(t, req, 1)
	quantity = req[corev1.ResourceStorage]
	assert.Equal(t, int64(5000000000), (&quantity).Value())

	// value is not provided and default is empty - the parameter must not be set at all
	podSpec = mdbv1.NewEmptyPodSpecWrapperBuilder().Build()
	req = buildStorageRequirements(podSpec.Persistence.SingleConfig, *podSpec.Default.Persistence.SingleConfig)

	assert.Len(t, req, 0)
}

func TestBuildLimitsRequirements(t *testing.T) {
	// values are provided - the defaults are ignored
	podSpec := mdbv1.NewEmptyPodSpecWrapperBuilder().SetCpuLimit("0.1").SetMemoryLimit("512M").
		SetDefault(mdbv1.NewPodSpecWrapperBuilder().SetCpuLimit("0.5").SetMemoryLimit("1G")).
		Build()

	req := buildLimitsRequirements(podSpec)

	assert.Len(t, req, 2)
	cpu := req[corev1.ResourceCPU]
	memory := req[corev1.ResourceMemory]
	assert.Equal(t, "100m", (&cpu).String())
	assert.Equal(t, int64(512000000), (&memory).Value())

	// values are not provided - the defaults are used
	podSpec = mdbv1.NewEmptyPodSpecWrapperBuilder().
		SetDefault(mdbv1.NewPodSpecWrapperBuilder().SetCpuLimit("0.8").SetMemoryLimit("10G")).
		Build()

	req = buildLimitsRequirements(podSpec)

	assert.Len(t, req, 2)
	cpu = req[corev1.ResourceCPU]
	memory = req[corev1.ResourceMemory]
	assert.Equal(t, "800m", (&cpu).String())
	assert.Equal(t, int64(10000000000), (&memory).Value())

	// value are not provided and default are empty - the parameters must not be set at all
	podSpec = mdbv1.NewEmptyPodSpecWrapperBuilder().Build()
	req = buildLimitsRequirements(podSpec)

	assert.Len(t, req, 0)
}

func TestBuildRequestsRequirements(t *testing.T) {
	// values are provided - the defaults are ignored
	podSpec := mdbv1.NewEmptyPodSpecWrapperBuilder().SetCpuRequests("0.1").SetMemoryRequest("512M").
		SetDefault(mdbv1.NewPodSpecWrapperBuilder().SetCpuRequests("0.5").SetMemoryRequest("1G")).
		Build()

	req := buildRequestsRequirements(podSpec)

	assert.Len(t, req, 2)
	cpu := req[corev1.ResourceCPU]
	memory := req[corev1.ResourceMemory]
	assert.Equal(t, "100m", (&cpu).String())
	assert.Equal(t, int64(512000000), (&memory).Value())

	// values are not provided - the defaults are used
	podSpec = mdbv1.NewEmptyPodSpecWrapperBuilder().
		SetDefault(mdbv1.NewPodSpecWrapperBuilder().SetCpuRequests("0.8").SetMemoryRequest("10G")).
		Build()
	req = buildRequestsRequirements(podSpec)

	assert.Len(t, req, 2)
	cpu = req[corev1.ResourceCPU]
	memory = req[corev1.ResourceMemory]
	assert.Equal(t, "800m", (&cpu).String())
	assert.Equal(t, int64(10000000000), (&memory).Value())

	// value are not provided and default are empty - the parameters must not be set at all
	podSpec = mdbv1.NewEmptyPodSpecWrapperBuilder().Build()
	req = buildRequestsRequirements(podSpec)

	assert.Len(t, req, 0)
}
