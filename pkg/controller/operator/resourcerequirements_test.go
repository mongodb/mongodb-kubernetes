package operator

import (
	"testing"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

func TestStorageRequirements(t *testing.T) {
	// value is provided - the default is ignored
	podSpec := mdbv1.PodSpecWrapper{
		MongoDbPodSpec: mdbv1.MongoDbPodSpec{MongoDbPodSpecStandard: mdbv1.MongoDbPodSpecStandard{
			Persistence: &mdbv1.Persistence{SingleConfig: &mdbv1.PersistenceConfig{Storage: "40G"}}}, PodAntiAffinityTopologyKey: ""},
		Default: mdbv1.MongoDbPodSpec{MongoDbPodSpecStandard: mdbv1.MongoDbPodSpecStandard{
			Persistence: &mdbv1.Persistence{SingleConfig: &mdbv1.PersistenceConfig{Storage: "12G"}}}, PodAntiAffinityTopologyKey: ""}}
	req := buildStorageRequirements(podSpec.Persistence.SingleConfig, podSpec.Default.Persistence.SingleConfig)

	assert.Len(t, req, 1)
	quantity := req[corev1.ResourceStorage]
	assert.Equal(t, int64(40000000000), (&quantity).Value())

	// value is not provided - the default is used
	podSpec = mdbv1.PodSpecWrapper{
		MongoDbPodSpec: mdbv1.MongoDbPodSpec{MongoDbPodSpecStandard: mdbv1.MongoDbPodSpecStandard{
			Persistence: &mdbv1.Persistence{SingleConfig: &mdbv1.PersistenceConfig{}}}, PodAntiAffinityTopologyKey: ""},
		Default: mdbv1.MongoDbPodSpec{MongoDbPodSpecStandard: mdbv1.MongoDbPodSpecStandard{
			Persistence: &mdbv1.Persistence{SingleConfig: &mdbv1.PersistenceConfig{Storage: "5G"}}}, PodAntiAffinityTopologyKey: ""}}
	req = buildStorageRequirements(podSpec.Persistence.SingleConfig, podSpec.Default.Persistence.SingleConfig)

	assert.Len(t, req, 1)
	quantity = req[corev1.ResourceStorage]
	assert.Equal(t, int64(5000000000), (&quantity).Value())

	// value is not provided and default is empty - the parameter must not be set at all
	podSpec = mdbv1.PodSpecWrapper{MongoDbPodSpec: mdbv1.MongoDbPodSpec{MongoDbPodSpecStandard: mdbv1.MongoDbPodSpecStandard{
		Persistence: &mdbv1.Persistence{SingleConfig: &mdbv1.PersistenceConfig{}}}, PodAntiAffinityTopologyKey: ""},
		Default: mdbv1.MongoDbPodSpec{MongoDbPodSpecStandard: mdbv1.MongoDbPodSpecStandard{
			Persistence: &mdbv1.Persistence{SingleConfig: &mdbv1.PersistenceConfig{}}}, PodAntiAffinityTopologyKey: ""}}
	req = buildStorageRequirements(podSpec.Persistence.SingleConfig, podSpec.Default.Persistence.SingleConfig)

	assert.Len(t, req, 0)
}

func TestBuildLimitsRequirements(t *testing.T) {
	// values are provided - the defaults are ignored
	podSpec := mdbv1.PodSpecWrapper{
		MongoDbPodSpec: newMongoDbPodSpec(mdbv1.MongoDbPodSpecStandard{Cpu: "0.1", Memory: "512M"}, ""),
		Default:        newMongoDbPodSpec(mdbv1.MongoDbPodSpecStandard{Cpu: "0.5", Memory: "1G"}, "")}
	req := buildLimitsRequirements(podSpec)

	assert.Len(t, req, 2)
	cpu := req[corev1.ResourceCPU]
	memory := req[corev1.ResourceMemory]
	assert.Equal(t, "100m", (&cpu).String())
	assert.Equal(t, int64(512000000), (&memory).Value())

	// values are not provided - the defaults are used
	podSpec = mdbv1.PodSpecWrapper{
		MongoDbPodSpec: mdbv1.MongoDbPodSpec{},
		Default:        newMongoDbPodSpec(mdbv1.MongoDbPodSpecStandard{Cpu: "0.8", Memory: "10G"}, "")}
	req = buildLimitsRequirements(podSpec)

	assert.Len(t, req, 2)
	cpu = req[corev1.ResourceCPU]
	memory = req[corev1.ResourceMemory]
	assert.Equal(t, "800m", (&cpu).String())
	assert.Equal(t, int64(10000000000), (&memory).Value())

	// value are not provided and default are empty - the parameters must not be set at all
	podSpec = mdbv1.PodSpecWrapper{MongoDbPodSpec: mdbv1.MongoDbPodSpec{}, Default: mdbv1.MongoDbPodSpec{}}
	req = buildLimitsRequirements(podSpec)

	assert.Len(t, req, 0)
}

func TestBuildRequestsRequirements(t *testing.T) {
	// values are provided - the defaults are ignored
	podSpec := mdbv1.PodSpecWrapper{
		MongoDbPodSpec: newMongoDbPodSpec(mdbv1.MongoDbPodSpecStandard{CpuRequests: "0.1", MemoryRequests: "512M"}, ""),
		Default:        newMongoDbPodSpec(mdbv1.MongoDbPodSpecStandard{CpuRequests: "0.5", MemoryRequests: "1G"}, "")}
	req := buildRequestsRequirements(podSpec)

	assert.Len(t, req, 2)
	cpu := req[corev1.ResourceCPU]
	memory := req[corev1.ResourceMemory]
	assert.Equal(t, "100m", (&cpu).String())
	assert.Equal(t, int64(512000000), (&memory).Value())

	// values are not provided - the defaults are used
	podSpec = mdbv1.PodSpecWrapper{
		MongoDbPodSpec: mdbv1.MongoDbPodSpec{},
		Default:        newMongoDbPodSpec(mdbv1.MongoDbPodSpecStandard{CpuRequests: "0.8", MemoryRequests: "10G"}, "")}
	req = buildRequestsRequirements(podSpec)

	assert.Len(t, req, 2)
	cpu = req[corev1.ResourceCPU]
	memory = req[corev1.ResourceMemory]
	assert.Equal(t, "800m", (&cpu).String())
	assert.Equal(t, int64(10000000000), (&memory).Value())

	// value are not provided and default are empty - the parameters must not be set at all
	podSpec = mdbv1.PodSpecWrapper{MongoDbPodSpec: mdbv1.MongoDbPodSpec{}, Default: mdbv1.MongoDbPodSpec{}}
	req = buildRequestsRequirements(podSpec)

	assert.Len(t, req, 0)
}

func newMongoDbPodSpec(spec mdbv1.MongoDbPodSpecStandard, podAntiAffinityTopologyKey string) mdbv1.MongoDbPodSpec {
	return mdbv1.MongoDbPodSpec{MongoDbPodSpecStandard: spec, PodAntiAffinityTopologyKey: podAntiAffinityTopologyKey}
}
