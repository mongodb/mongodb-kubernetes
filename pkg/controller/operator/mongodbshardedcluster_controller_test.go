package operator

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"github.com/stretchr/testify/assert"

	"reflect"

	"os"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestShardedClusterEventMethodsHandlePanic(t *testing.T) {
	// nullifying env variable will result in panic exception raised
	os.Setenv(util.AutomationAgentImageUrl, "")
	st := DefaultClusterBuilder().Build()

	manager := newMockedManager(st)
	checkReconcileFailed(t, newShardedClusterReconciler(manager, om.NewEmptyMockedOmConnection), st, "Failed to reconcile Sharded Cluster", manager.client)

	// restoring
	InitDefaultEnvVariables()
}

func TestReconcileCreateShardedCluster(t *testing.T) {
	sc := DefaultClusterBuilder().Build()

	manager := newMockedManager(sc)
	client := manager.client

	reconciler := newShardedClusterReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, sc, client)

	assert.Len(t, client.secrets, 2)
	assert.Len(t, client.services, 4)
	assert.Len(t, client.sets, 4)
	assert.Equal(t, *client.getSet(objectKey(sc.Namespace, sc.ConfigRsName())).Spec.Replicas, int32(sc.Spec.ConfigServerCount))
	assert.Equal(t, *client.getSet(objectKey(sc.Namespace, sc.MongosRsName())).Spec.Replicas, int32(sc.Spec.MongosCount))
	assert.Equal(t, *client.getSet(objectKey(sc.Namespace, sc.ShardRsName(0))).Spec.Replicas, int32(sc.Spec.MongodsPerShardCount))
	assert.Equal(t, *client.getSet(objectKey(sc.Namespace, sc.ShardRsName(1))).Spec.Replicas, int32(sc.Spec.MongodsPerShardCount))

	connection := om.CurrMockedConnection
	connection.CheckDeployment(t, createDeploymentFromShardedCluster(sc))
	connection.CheckNumberOfUpdateRequests(t, 1)
	// we don't remove hosts from monitoring if there is no scale down
	connection.CheckOperationsDidntHappen(t, reflect.ValueOf(connection.GetHosts), reflect.ValueOf(connection.RemoveHost))
}

// TestAddDeleteShardedCluster checks that no state is left in OpsManager on removal of the sharded cluster
func TestAddDeleteShardedCluster(t *testing.T) {
	// First we need to create a sharded cluster
	sc := DefaultClusterBuilder().Build()

	kubeManager := newMockedManager(sc)
	reconciler := newShardedClusterReconciler(kubeManager, om.NewEmptyMockedOmConnectionWithDelay)

	checkReconcileSuccessful(t, reconciler, sc, kubeManager.client)
	omConn := om.CurrMockedConnection
	omConn.CleanHistory()

	// Now delete it
	assert.NoError(t, reconciler.delete(sc, zap.S()))

	// Operator doesn't mutate K8s state, so we don't check its changes, only OM
	omConn.CheckResourcesDeleted(t)

	omConn.CheckOrderOfOperations(t,
		reflect.ValueOf(omConn.ReadUpdateDeployment), reflect.ValueOf(omConn.ReadAutomationStatus),
		reflect.ValueOf(omConn.ReadBackupConfigs), reflect.ValueOf(omConn.GetHosts), reflect.ValueOf(omConn.RemoveHost))

}

// TestPrepareScaleDownShardedCluster tests the scale down operation for config servers and mongods per shard. It checks
// that all members that will be removed are marked as unvoted
func TestPrepareScaleDownShardedCluster_ConfigMongodsUp(t *testing.T) {
	sc := DefaultClusterBuilder().
		SetConfigServerCountStatus(3).
		SetConfigServerCountSpec(2).
		SetMongodsPerShardCountStatus(4).
		SetMongodsPerShardCountSpec(3).
		Build()
	newState := createStateFromResource(sc)

	oldDeployment := createDeploymentFromShardedCluster(sc)
	mockedOmConnection := om.NewMockedOmConnection(oldDeployment)
	assert.NoError(t, prepareScaleDownShardedCluster(mockedOmConnection, newState, sc, zap.S()))

	// expected change of state: rs members are marked unvoted
	expectedDeployment := createDeploymentFromShardedCluster(sc)
	firstConfig := sc.ConfigRsName() + "-2"
	firstShard := sc.ShardRsName(0) + "-3"
	secondShard := sc.ShardRsName(1) + "-3"

	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(sc.ConfigRsName(), []string{firstConfig}))
	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(sc.ShardRsName(0), []string{firstShard}))
	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(sc.ShardRsName(1), []string{secondShard}))

	mockedOmConnection.CheckNumberOfUpdateRequests(t, 1)
	mockedOmConnection.CheckDeployment(t, expectedDeployment)
	// we don't remove hosts from monitoring at this stage
	mockedOmConnection.CheckMonitoredHostsRemoved(t, []string{})
}

// TestPrepareScaleDownShardedCluster_ShardsUpMongodsDown checks the situation when shards count increases and mongods
// count per shard is decreased - scale down operation is expected to be called only for existing shards
func TestPrepareScaleDownShardedCluster_ShardsUpMongodsDown(t *testing.T) {
	sc := DefaultClusterBuilder().
		SetShardCountStatus(2).
		SetShardCountSpec(4).
		SetMongodsPerShardCountStatus(4).
		SetMongodsPerShardCountSpec(3).
		Build()
	newState := createStateFromResource(sc)

	oldDeployment := createDeploymentFromShardedCluster(sc)
	mockedOmConnection := om.NewMockedOmConnection(oldDeployment)
	assert.NoError(t, prepareScaleDownShardedCluster(mockedOmConnection, newState, sc, zap.S()))

	// expected change of state: rs members are marked unvoted only for two shards (old state)
	expectedDeployment := createDeploymentFromShardedCluster(sc)
	firstShard := sc.ShardRsName(0) + "-3"
	secondShard := sc.ShardRsName(1) + "-3"

	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(sc.ShardRsName(0), []string{firstShard}))
	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(sc.ShardRsName(1), []string{secondShard}))

	mockedOmConnection.CheckNumberOfUpdateRequests(t, 1)
	mockedOmConnection.CheckDeployment(t, expectedDeployment)
	// we don't remove hosts from monitoring at this stage
	mockedOmConnection.CheckOperationsDidntHappen(t, reflect.ValueOf(mockedOmConnection.RemoveHost))
}

// TestPrepareScaleDownShardedCluster_OnlyMongos checks that if only mongos processes are scaled down - then no preliminary
// actions are done
func TestPrepareScaleDownShardedCluster_OnlyMongos(t *testing.T) {
	sc := DefaultClusterBuilder().SetMongosCountStatus(4).SetMongosCountSpec(2).Build()

	newState := createStateFromResource(sc)

	oldDeployment := createDeploymentFromShardedCluster(sc)
	mockedOmConnection := om.NewMockedOmConnection(oldDeployment)
	assert.NoError(t, prepareScaleDownShardedCluster(mockedOmConnection, newState, sc, zap.S()))

	mockedOmConnection.CheckNumberOfUpdateRequests(t, 0)
	mockedOmConnection.CheckDeployment(t, createDeploymentFromShardedCluster(sc))
	mockedOmConnection.CheckOperationsDidntHappen(t, reflect.ValueOf(mockedOmConnection.RemoveHost))
}

// TestUpdateOmDeploymentShardedCluster_HostsRemovedFromMonitoring verifies that if scale down operation was performed -
// hosts are removed
func TestUpdateOmDeploymentShardedCluster_HostsRemovedFromMonitoring(t *testing.T) {
	sc := DefaultClusterBuilder().
		SetMongosCountStatus(3).
		SetMongosCountSpec(1).
		SetConfigServerCountStatus(4).
		SetConfigServerCountSpec(3).
		Build()

	newState := createStateFromResource(sc)

	mockOm := om.NewMockedOmConnection(createDeploymentFromShardedCluster(sc))
	assert.NoError(t, updateOmDeploymentShardedCluster(mockOm, sc, newState, zap.S()))

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadUpdateDeployment), reflect.ValueOf(mockOm.RemoveHost))

	// expected change of state: no unvoting - just monitoring deleted
	firstConfig := sc.ConfigRsName() + "-3"
	firstMongos := sc.MongosRsName() + "-1"
	secondMongos := sc.MongosRsName() + "-2"

	mockOm.CheckMonitoredHostsRemoved(t, []string{
		firstConfig + ".slaney-cs.mongodb.svc.cluster.local",
		firstMongos + ".slaney-svc.mongodb.svc.cluster.local",
		secondMongos + ".slaney-svc.mongodb.svc.cluster.local",
	})
}

// CLOUDP-32765: checks that pod anti affinity rule spreads mongods inside one shard, not inside all shards
func TestPodAntiaffinity_MongodsInsideShardAreSpread(t *testing.T) {
	sc := DefaultClusterBuilder().Build()

	reconciler := newShardedClusterReconciler(newMockedManager(sc), om.NewEmptyMockedOmConnection)
	state := reconciler.buildKubeObjectsForShardedCluster(sc, defaultPodVars(), zap.S())

	shardHelpers := state.shardsSetsHelpers

	assert.Len(t, shardHelpers, 2)

	firstShardSet := shardHelpers[0].BuildStatefulSet()
	secondShardSet := shardHelpers[1].BuildStatefulSet()

	assert.Equal(t, sc.ShardRsName(0), firstShardSet.Spec.Selector.MatchLabels[POD_ANTI_AFFINITY_LABEL_KEY])
	assert.Equal(t, sc.ShardRsName(1), secondShardSet.Spec.Selector.MatchLabels[POD_ANTI_AFFINITY_LABEL_KEY])

	firstShartPodAffinityTerm := firstShardSet.Spec.Template.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm
	assert.Equal(t, firstShartPodAffinityTerm.LabelSelector.MatchLabels[POD_ANTI_AFFINITY_LABEL_KEY], sc.ShardRsName(0))

	secondShartPodAffinityTerm := secondShardSet.Spec.Template.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm
	assert.Equal(t, secondShartPodAffinityTerm.LabelSelector.MatchLabels[POD_ANTI_AFFINITY_LABEL_KEY], sc.ShardRsName(1))
}

func createDeploymentFromShardedCluster(sh *v1.MongoDB) om.Deployment {
	state := createStateFromResource(sh)
	mongosProcesses := createProcesses(state.mongosSetHelper.BuildStatefulSet(), sh.Spec.ClusterName, sh.Spec.Version, om.ProcessTypeMongos)
	configRs := buildReplicaSetFromStatefulSet(state.configSrvSetHelper.BuildStatefulSet(), sh.Spec.ClusterName, sh.Spec.Version)
	shards := make([]om.ReplicaSetWithProcesses, len(state.shardsSetsHelpers))
	for i, s := range state.shardsSetsHelpers {
		shards[i] = buildReplicaSetFromStatefulSet(s.BuildStatefulSet(), sh.Spec.ClusterName, sh.Spec.Version)
	}

	d := om.NewDeployment()
	d.MergeShardedCluster(sh.Name, mongosProcesses, configRs, shards)
	d.AddMonitoringAndBackup(mongosProcesses[0].HostName(), zap.S())
	return d
}

// createStateFromResource creates the kube state for the sharded cluster. Note, that it uses the `Status` of cluster
// instead of `Spec` as it tries to reflect the CURRENT state
func createStateFromResource(sh *v1.MongoDB) ShardedClusterKubeState {
	shardHelpers := make([]*StatefulSetHelper, sh.Status.ShardCount)
	for i := 0; i < sh.Status.ShardCount; i++ {
		shardHelpers[i] = defaultSetHelper().SetName(sh.ShardRsName(i)).SetService(sh.ShardServiceName()).SetReplicas(sh.Status.MongodsPerShardCount)
	}
	return ShardedClusterKubeState{
		mongosSetHelper:    defaultSetHelper().SetName(sh.MongosRsName()).SetService(sh.ServiceName()).SetReplicas(sh.Status.MongosCount),
		configSrvSetHelper: defaultSetHelper().SetName(sh.ConfigRsName()).SetService(sh.ConfigSrvServiceName()).SetReplicas(sh.Status.ConfigServerCount),
		shardsSetsHelpers:  shardHelpers}
}

type ClusterBuilder struct {
	*v1.MongoDB
}

func DefaultClusterBuilder() *ClusterBuilder {
	sizeConfig := v1.MongodbShardedClusterSizeConfig{
		ShardCount:           2,
		MongodsPerShardCount: 3,
		ConfigServerCount:    3,
		MongosCount:          4,
	}

	status := v1.MongoDbStatus{
		MongodbShardedClusterSizeConfig: sizeConfig,
	}

	spec := v1.MongoDbSpec{
		Persistent:                      util.BooleanRef(false),
		Project:                         TestProjectConfigMapName,
		Credentials:                     TestCredentialsSecretName,
		Version:                         "3.6.4",
		ResourceType:                    v1.ShardedCluster,
		MongodbShardedClusterSizeConfig: sizeConfig,
	}

	resource := &v1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "slaney", Namespace: TestNamespace},
		Status:     status,
		Spec:       spec,
	}

	return &ClusterBuilder{resource}
}

func (b *ClusterBuilder) SetName(name string) *ClusterBuilder {
	b.Name = name
	return b
}
func (b *ClusterBuilder) SetShardCountSpec(count int) *ClusterBuilder {
	b.Spec.ShardCount = count
	return b
}
func (b *ClusterBuilder) SetMongodsPerShardCountSpec(count int) *ClusterBuilder {
	b.Spec.MongodsPerShardCount = count
	return b
}
func (b *ClusterBuilder) SetConfigServerCountSpec(count int) *ClusterBuilder {
	b.Spec.ConfigServerCount = count
	return b
}
func (b *ClusterBuilder) SetMongosCountSpec(count int) *ClusterBuilder {
	b.Spec.MongosCount = count
	return b
}
func (b *ClusterBuilder) SetShardCountStatus(count int) *ClusterBuilder {
	b.Status.ShardCount = count
	return b
}
func (b *ClusterBuilder) SetMongodsPerShardCountStatus(count int) *ClusterBuilder {
	b.Status.MongodsPerShardCount = count
	return b
}
func (b *ClusterBuilder) SetConfigServerCountStatus(count int) *ClusterBuilder {
	b.Status.ConfigServerCount = count
	return b
}
func (b *ClusterBuilder) SetMongosCountStatus(count int) *ClusterBuilder {
	b.Status.MongosCount = count
	return b
}
func (b *ClusterBuilder) Build() *v1.MongoDB {
	b.Spec.ResourceType = v1.ShardedCluster
	b.InitDefaults()
	return b.MongoDB
}
