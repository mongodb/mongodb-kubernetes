package operator

import (
	"testing"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
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
	podSpec := mongodb.PodSpecWrapper{
		MongoDbPodSpec: mongodb.MongoDbPodSpec{MongoDbPodSpecStandard: mongodb.MongoDbPodSpecStandard{
			Persistence: &mongodb.Persistence{SingleConfig: &mongodb.PersistenceConfig{Storage: "40G"}}}, PodAntiAffinityTopologyKey: ""},
		Default: mongodb.MongoDbPodSpec{MongoDbPodSpecStandard: mongodb.MongoDbPodSpecStandard{
			Persistence: &mongodb.Persistence{SingleConfig: &mongodb.PersistenceConfig{Storage: "12G"}}}, PodAntiAffinityTopologyKey: ""}}
	req := buildStorageRequirements(podSpec.Persistence.SingleConfig, podSpec.Default.Persistence.SingleConfig)

	assert.Len(t, req, 1)
	quantity := req[corev1.ResourceStorage]
	assert.Equal(t, int64(40000000000), (&quantity).Value())

	// value is not provided - the default is used
	podSpec = mongodb.PodSpecWrapper{
		MongoDbPodSpec: mongodb.MongoDbPodSpec{MongoDbPodSpecStandard: mongodb.MongoDbPodSpecStandard{
			Persistence: &mongodb.Persistence{SingleConfig: &mongodb.PersistenceConfig{}}}, PodAntiAffinityTopologyKey: ""},
		Default: mongodb.MongoDbPodSpec{MongoDbPodSpecStandard: mongodb.MongoDbPodSpecStandard{
			Persistence: &mongodb.Persistence{SingleConfig: &mongodb.PersistenceConfig{Storage: "5G"}}}, PodAntiAffinityTopologyKey: ""}}
	req = buildStorageRequirements(podSpec.Persistence.SingleConfig, podSpec.Default.Persistence.SingleConfig)

	assert.Len(t, req, 1)
	quantity = req[corev1.ResourceStorage]
	assert.Equal(t, int64(5000000000), (&quantity).Value())

	// value is not provided and default is empty - the parameter must not be set at all
	podSpec = mongodb.PodSpecWrapper{MongoDbPodSpec: mongodb.MongoDbPodSpec{MongoDbPodSpecStandard: mongodb.MongoDbPodSpecStandard{
		Persistence: &mongodb.Persistence{SingleConfig: &mongodb.PersistenceConfig{}}}, PodAntiAffinityTopologyKey: ""},
		Default: mongodb.MongoDbPodSpec{MongoDbPodSpecStandard: mongodb.MongoDbPodSpecStandard{
			Persistence: &mongodb.Persistence{SingleConfig: &mongodb.PersistenceConfig{}}}, PodAntiAffinityTopologyKey: ""}}
	req = buildStorageRequirements(podSpec.Persistence.SingleConfig, podSpec.Default.Persistence.SingleConfig)

	assert.Len(t, req, 0)
}

func TestPodRequirements(t *testing.T) {
	// values are provided - the defaults are ignored
	podSpec := mongodb.PodSpecWrapper{
		MongoDbPodSpec: mongodb.MongoDbPodSpec{MongoDbPodSpecStandard: mongodb.MongoDbPodSpecStandard{Cpu: "0.1", Memory: "512M"}, PodAntiAffinityTopologyKey: ""},
		Default:        mongodb.MongoDbPodSpec{MongoDbPodSpecStandard: mongodb.MongoDbPodSpecStandard{Cpu: "0.5", Memory: "1G"}, PodAntiAffinityTopologyKey: ""}}
	req := buildRequirements(podSpec)

	assert.Len(t, req, 2)
	cpu := req[corev1.ResourceCPU]
	memory := req[corev1.ResourceMemory]
	assert.Equal(t, "100m", (&cpu).String())
	assert.Equal(t, int64(512000000), (&memory).Value())

	// values are not provided - the defaults are used
	podSpec = mongodb.PodSpecWrapper{
		MongoDbPodSpec: mongodb.MongoDbPodSpec{},
		Default:        mongodb.MongoDbPodSpec{MongoDbPodSpecStandard: mongodb.MongoDbPodSpecStandard{Cpu: "0.8", Memory: "10G"}, PodAntiAffinityTopologyKey: ""}}
	req = buildRequirements(podSpec)

	assert.Len(t, req, 2)
	cpu = req[corev1.ResourceCPU]
	memory = req[corev1.ResourceMemory]
	assert.Equal(t, "800m", (&cpu).String())
	assert.Equal(t, int64(10000000000), (&memory).Value())

	// value are not provided and default are empty - the parameters must not be set at all
	podSpec = mongodb.PodSpecWrapper{MongoDbPodSpec: mongodb.MongoDbPodSpec{}, Default: mongodb.MongoDbPodSpec{}}
	req = buildRequirements(podSpec)

	assert.Len(t, req, 0)
}
