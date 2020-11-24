package om

import (
	"errors"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"
	"github.com/stretchr/testify/assert"
)

func TestOpsManager_RunValidations_OpsManager(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.None)
	assert.Nil(t, err)
	assert.Equal(t, 0, len(om.Status.OpsManagerStatus.Warnings))
}

func TestOpsManager_RunValidations_AppDWithConnectivity(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.Connectivity = &mdbv1.MongoDBConnectivity{ReplicaSetHorizons: []mdbv1.MongoDBHorizonConfig{}}
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.AppDb)
	assert.Error(t, err)
	assert.Equal(t, errors.New("connectivity field is not configurable for application databases"), err)
}

func TestOpsManager_RunValidations_AppDWithCredentials(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.Credentials = "something"
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.AppDb)
	assert.Error(t, err)
	assert.Equal(t, errors.New("credentials field is not configurable for application databases"), err)
}

func TestOpsManager_RunValidations_AppDWithOpsManagerConfig(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.OpsManagerConfig = &mdbv1.PrivateCloudConfig{}
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.AppDb)
	assert.Error(t, err)
	assert.Equal(t, errors.New("opsManager field is not configurable for application databases"), err)
}

func TestOpsManager_RunValidations_AppDWithCloudManagerConfig(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.CloudManagerConfig = &mdbv1.PrivateCloudConfig{}
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.AppDb)
	assert.Error(t, err)
	assert.Equal(t, errors.New("cloudManager field is not configurable for application databases"), err)
}

func TestOpsManager_RunValidations_AppDWithProjectName(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.Project = "something"
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.AppDb)
	assert.Error(t, err)
	assert.Equal(t, errors.New("project field is not configurable for application databases"), err)
}

func TestOpsManager_RunValidations_AppDWithConfigSrvPodSpec(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.ConfigSrvPodSpec = &mdbv1.MongoDbPodSpec{}
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.AppDb)
	assert.Error(t, err)
	assert.Equal(t, errors.New("configSrvPodSpec field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"), err)
}

func TestOpsManager_RunValidations_AppDWithMongosPodSpec(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.MongosPodSpec = &mdbv1.MongoDbPodSpec{}
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.AppDb)
	assert.Error(t, err)
	assert.Equal(t, errors.New("mongosPodSpec field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"), err)
}

func TestOpsManager_RunValidations_AppDWithShardPodSpec(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.ShardPodSpec = &mdbv1.MongoDbPodSpec{}
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.AppDb)
	assert.Error(t, err)
	assert.Equal(t, errors.New("shardPodSpec field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"), err)
}

func TestOpsManager_RunValidations_AppDWithShardCount(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.ShardCount = 1
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.AppDb)
	assert.Error(t, err)
	assert.Equal(t, errors.New("shardCount field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"), err)
}

func TestOpsManager_RunValidations_AppDWithMongodsPerShardCount(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.MongodsPerShardCount = 1
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.AppDb)
	assert.Error(t, err)
	assert.Equal(t, errors.New("mongodsPerShardCount field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"), err)
}

func TestOpsManager_RunValidations_AppDWithMongosCount(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.MongosCount = 1
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.AppDb)
	assert.Error(t, err)
	assert.Equal(t, errors.New("mongosCount field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"), err)
}

func TestOpsManager_RunValidations_AppDWithConfigServerCount(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.ConfigServerCount = 1
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.AppDb)
	assert.Error(t, err)
	assert.Equal(t, errors.New("configServerCount field is not configurable for application databases as it is for sharded clusters and appdbs are replica sets"), err)
}

func TestOpsManager_RunValidations_S3StoreUserResourceRef(t *testing.T) {
	config := S3Config{Name: "test", MongoDBUserRef: &MongoDBUserRef{Name: "foo"}}
	om := NewOpsManagerBuilderDefault().AddS3SnapshotStore(config).Build()
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.OpsManager)
	assert.Error(t, err)
	assert.Equal(t, errors.New("'mongodbResourceRef' must be specified if 'mongodbUserRef' is configured (S3 Store: test)"), err)
}

func TestOpsManager_RunValidations_InvalidVersion(t *testing.T) {
	om := NewOpsManagerBuilder().SetVersion("4.4").Build()
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.OpsManager)
	assert.Equal(t, errors.New("'4.4' is an invalid value for spec.version: Ops Manager Status spec.version 4.4 is invalid"), err)
}

func TestOpsManager_RunValidations_InvalidAppDBVersion(t *testing.T) {
	om := NewOpsManagerBuilderDefault().SetAppDbVersion("4.0.0").Build()
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, status.None, part)
	assert.NoError(t, err)
	om = NewOpsManagerBuilderDefault().SetAppDbVersion("4.2.0-rc1").Build()
	err, part = om.ProcessValidationsOnReconcile()
	assert.Equal(t, status.None, part)
	assert.NoError(t, err)
	om = NewOpsManagerBuilderDefault().SetAppDbVersion("4.5.0-ent").Build()
	err, part = om.ProcessValidationsOnReconcile()
	assert.Equal(t, status.None, part)
	assert.NoError(t, err)

	om = NewOpsManagerBuilderDefault().SetAppDbVersion("3.6.12").Build()
	err, part = om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.AppDb)
	assert.Equal(t, errors.New("the version of Application Database must be >= 4.0"), err)
	om = NewOpsManagerBuilderDefault().SetAppDbVersion("foo").Build()
	err, part = om.ProcessValidationsOnReconcile()
	assert.Equal(t, part, status.AppDb)
	assert.Equal(t, errors.New("'foo' is an invalid value for spec.applicationDatabase.version: No Major.Minor.Patch elements found"), err)
}

func TestOpsManager_RunValidations_InvalidPrerelease(t *testing.T) {
	om := NewOpsManagerBuilder().SetVersion("3.5.0-1193-x86_64").Build()
	version, err := versionutil.StringToSemverVersion(om.Spec.Version)
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, status.None, part)
	assert.NoError(t, err)
	assert.NoError(t, err)
	assert.Equal(t, uint64(3), version.Major)
	assert.Equal(t, uint64(5), version.Minor)
	assert.Equal(t, uint64(0), version.Patch)
}

func TestOpsManager_RunValidations_InvalidMajor(t *testing.T) {
	om := NewOpsManagerBuilder().SetVersion("4_4.4.0").Build()
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, status.OpsManager, part)
	assert.Equal(t, errors.New("'4_4.4.0' is an invalid value for spec.version: Ops Manager Status spec.version 4_4.4.0 is invalid"), err)
}

func TestOpsManager_RunValidations_InvalidMinor(t *testing.T) {
	om := NewOpsManagerBuilder().SetVersion("4.4_4.0").Build()
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, status.OpsManager, part)
	assert.Equal(t, errors.New("'4.4_4.0' is an invalid value for spec.version: Ops Manager Status spec.version 4.4_4.0 is invalid"), err)
}

func TestOpsManager_RunValidations_InvalidPatch(t *testing.T) {
	om := NewOpsManagerBuilder().SetVersion("4.4.4_0").Build()
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, status.OpsManager, part)
	assert.Equal(t, errors.New("'4.4.4_0' is an invalid value for spec.version: Ops Manager Status spec.version 4.4.4_0 is invalid"), err)
}

func TestOpsManager_RunValidations_MultipleWarnings(t *testing.T) {
	om := NewOpsManagerBuilderDefault().Build()
	om.Spec.AppDB.Project = "something"
	om.Spec.AppDB.ConfigServerCount = 1
	err, part := om.ProcessValidationsOnReconcile()
	assert.Equal(t, status.AppDb, part)
	assert.Error(t, err)
	assert.Equal(t, errors.New("project field is not configurable for application databases"), err)
}
