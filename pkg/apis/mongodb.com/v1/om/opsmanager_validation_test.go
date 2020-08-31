package om

import (
	"errors"
	"fmt"
	"testing"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/stretchr/testify/assert"
)

func TestOpsManager_RunValidations_OpsManager(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	assert.Nil(t, om.ProcessValidationsOnReconcile())
	assert.Equal(t, 0, len(om.Status.Warnings))
}

func TestOpsManager_RunValidations_AppDWithConnectivity(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.Connectivity = &mdbv1.MongoDBConnectivity{ReplicaSetHorizons: []mdbv1.MongoDBHorizonConfig{}}
	assert.Nil(t, om.ProcessValidationsOnReconcile())
	assert.Contains(t, om.Status.Warnings, status.Warning("connectivity field is not configurable for application databases"))
}

func TestOpsManager_RunValidations_AppDWithCredentials(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.Credentials = "something"
	assert.Nil(t, om.ProcessValidationsOnReconcile())
	assert.Contains(t, om.Status.Warnings, status.Warning("credentials field is not configurable for application databases"))
}

func TestOpsManager_RunValidations_AppDWithOpsManagerConfig(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.OpsManagerConfig = &mdbv1.PrivateCloudConfig{}
	assert.Nil(t, om.ProcessValidationsOnReconcile())
	assert.Contains(t, om.Status.Warnings, status.Warning("opsManager field is not configurable for application databases"))
}

func TestOpsManager_RunValidations_AppDWithCloudManagerConfig(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.CloudManagerConfig = &mdbv1.PrivateCloudConfig{}
	assert.Nil(t, om.ProcessValidationsOnReconcile())
	assert.Contains(t, om.Status.Warnings, status.Warning("cloudManager field is not configurable for application databases"))
}

func TestOpsManager_RunValidations_AppDWithProjectName(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.Project = "something"
	assert.Nil(t, om.ProcessValidationsOnReconcile())
	assert.Contains(t, om.Status.Warnings, status.Warning("project field is not configurable for application databases"))
}

func TestOpsManager_RunValidations_AppDWithConfigSrvPodSpec(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.ConfigSrvPodSpec = &mdbv1.MongoDbPodSpec{}
	assert.Nil(t, om.ProcessValidationsOnReconcile())
	assert.Contains(t, om.Status.Warnings, status.Warning("configSrvPodSpec field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"))
}

func TestOpsManager_RunValidations_AppDWithMongosPodSpec(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.MongosPodSpec = &mdbv1.MongoDbPodSpec{}
	assert.Nil(t, om.ProcessValidationsOnReconcile())
	assert.Contains(t, om.Status.Warnings, status.Warning("mongosPodSpec field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"))
}

func TestOpsManager_RunValidations_AppDWithShardPodSpec(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.ShardPodSpec = &mdbv1.MongoDbPodSpec{}
	assert.Nil(t, om.ProcessValidationsOnReconcile())
	assert.Contains(t, om.Status.Warnings, status.Warning("shardPodSpec field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"))
}

func TestOpsManager_RunValidations_AppDWithShardCount(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.ShardCount = 1
	assert.Nil(t, om.ProcessValidationsOnReconcile())
	assert.Contains(t, om.Status.Warnings, status.Warning("shardCount field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"))
}

func TestOpsManager_RunValidations_AppDWithMongodsPerShardCount(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.MongodsPerShardCount = 1
	assert.Nil(t, om.ProcessValidationsOnReconcile())
	assert.Contains(t, om.Status.Warnings, status.Warning("mongodsPerShardCount field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"))
}

func TestOpsManager_RunValidations_AppDWithMongosCount(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.MongosCount = 1
	assert.Nil(t, om.ProcessValidationsOnReconcile())
	assert.Contains(t, om.Status.Warnings, status.Warning("mongosCount field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"))
}

func TestOpsManager_RunValidations_AppDWithConfigServerCount(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.ConfigServerCount = 1
	assert.Nil(t, om.ProcessValidationsOnReconcile())
	assert.Contains(t, om.Status.Warnings, status.Warning("configServerCount field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"))
}

func TestOpsManager_RunValidations_S3StoreUserResourceRef(t *testing.T) {
	config := S3Config{Name: "test", MongoDBUserRef: &MongoDBUserRef{Name: "foo"}}
	om := NewOpsManagerBuilderDefault().AddS3SnapshotStore(config).Build()
	assert.Nil(t, om.ProcessValidationsOnReconcile())
	assert.Contains(t, om.Status.Warnings, status.Warning("'mongodbResourceRef' must be specified if 'mongodbUserRef' is configured (S3 Store: test)"))
}

func TestOpsManager_RunValidations_InvalidVersion(t *testing.T) {
	om := NewOpsManagerBuilder().SetVersion("4.4").Build()
	assert.Equal(t, errors.New("'4.4' is an invalid value for spec.version: No Major.Minor.Patch elements found"), om.ProcessValidationsOnReconcile())
}

func TestOpsManager_RunValidations_MultipleWarnings(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.Project = "something"
	om.Spec.AppDB.ConfigServerCount = 1
	assert.Nil(t, om.ProcessValidationsOnReconcile())
	assert.Equal(t, fmt.Sprintf("%s", om.Status.Warnings), "[project field is not configurable for application databases; configServerCount field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets]")
}
