package wiredtiger

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
	mock.InitDefaultEnvVariables()
}

func TestWiredTigerCacheConversion(t *testing.T) {

	set := construct.DatabaseStatefulSet(*mdbv1.NewReplicaSetBuilder().SetPodSpec(&mdbv1.NewPodSpecWrapperBuilder().SetMemory("1800M").Build().MongoDbPodSpec).Build(), construct.ReplicaSetOptions())
	assert.Equal(t, float32(0.4), *CalculateCache(set, util.DatabaseContainerName, "4.0.0"))

	set = construct.DatabaseStatefulSet(*mdbv1.NewReplicaSetBuilder().SetPodSpec(&mdbv1.NewPodSpecWrapperBuilder().SetMemory("2900M").Build().MongoDbPodSpec).Build(), construct.ReplicaSetOptions())
	assert.Equal(t, float32(0.95), *CalculateCache(set, util.DatabaseContainerName, "4.0.4"))

	set = construct.DatabaseStatefulSet(*mdbv1.NewReplicaSetBuilder().SetPodSpec(&mdbv1.NewPodSpecWrapperBuilder().SetMemory("32G").Build().MongoDbPodSpec).Build(), construct.ReplicaSetOptions())
	assert.Equal(t, float32(15.5), *CalculateCache(set, util.DatabaseContainerName, "3.6.5"))

	set = construct.DatabaseStatefulSet(*mdbv1.NewReplicaSetBuilder().SetPodSpec(&mdbv1.NewPodSpecWrapperBuilder().SetMemory("55.832G").Build().MongoDbPodSpec).Build(), construct.ReplicaSetOptions())
	assert.Equal(t, float32(27.416), *CalculateCache(set, util.DatabaseContainerName, "3.6.12"))

	set = construct.DatabaseStatefulSet(*mdbv1.NewReplicaSetBuilder().SetPodSpec(&mdbv1.NewPodSpecWrapperBuilder().SetMemory("181G").Build().MongoDbPodSpec).Build(), construct.ReplicaSetOptions())
	assert.Equal(t, float32(90.0), *CalculateCache(set, util.DatabaseContainerName, "3.4.10"))

	// We round fractional part to two digits, here 256M were rounded to 0.26G

	set = construct.DatabaseStatefulSet(*mdbv1.NewReplicaSetBuilder().SetPodSpec(&mdbv1.NewPodSpecWrapperBuilder().SetMemory("300.65Mi").Build().MongoDbPodSpec).Build(), construct.ReplicaSetOptions())
	assert.Equal(t, float32(0.256), *CalculateCache(set, util.DatabaseContainerName, "4.0.8"))

	set = construct.DatabaseStatefulSet(*mdbv1.NewReplicaSetBuilder().SetPodSpec(&mdbv1.NewPodSpecWrapperBuilder().SetMemory("0G").Build().MongoDbPodSpec).Build(), construct.ReplicaSetOptions())
	assert.Nil(t, CalculateCache(set, util.DatabaseContainerName, "4.0.0"))

	// We don't calculate wired tiger cache for latest versions of mongodb

	set = construct.DatabaseStatefulSet(*mdbv1.NewReplicaSetBuilder().SetPodSpec(&mdbv1.NewPodSpecWrapperBuilder().SetMemory("32G").Build().MongoDbPodSpec).Build(), construct.ReplicaSetOptions())
	assert.Nil(t, CalculateCache(set, util.DatabaseContainerName, "4.2.0"))

	set = construct.DatabaseStatefulSet(*mdbv1.NewReplicaSetBuilder().SetPodSpec(&mdbv1.NewPodSpecWrapperBuilder().SetMemory("32G").Build().MongoDbPodSpec).Build(), construct.ReplicaSetOptions())
	assert.Nil(t, CalculateCache(set, util.DatabaseContainerName, "4.0.9"))

	set = construct.DatabaseStatefulSet(*mdbv1.NewReplicaSetBuilder().SetPodSpec(&mdbv1.NewPodSpecWrapperBuilder().SetMemory("32G").Build().MongoDbPodSpec).Build(), construct.ReplicaSetOptions())
	assert.Nil(t, CalculateCache(set, util.DatabaseContainerName, "3.6.13"))
}
