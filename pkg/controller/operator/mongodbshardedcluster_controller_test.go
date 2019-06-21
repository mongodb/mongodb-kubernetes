package operator

import (
	"context"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"github.com/stretchr/testify/assert"

	"reflect"

	"os"

	"fmt"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"go.uber.org/zap"
	certsv1 "k8s.io/api/certificates/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestShardedClusterEventMethodsHandlePanic(t *testing.T) {
	// nullifying env variable will result in panic exception raised
	os.Setenv(util.AutomationAgentImageUrl, "")
	st := DefaultClusterBuilder().Build()

	manager := newMockedManager(st)
	checkReconcileFailed(t, newShardedClusterReconciler(manager, om.NewEmptyMockedOmConnection), st,
		"Failed to reconcile Sharded Cluster: MONGODB_ENTERPRISE_DATABASE_IMAGE environment variable is not set!",
		manager.client)

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
	assert.Len(t, client.services, 3)
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

func TestReconcileCreateShardedCluster_ScaleDown(t *testing.T) {
	// First creation
	sc := DefaultClusterBuilder().SetShardCountSpec(4).Build()

	manager := newMockedManager(sc)
	client := manager.client

	reconciler := newShardedClusterReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, sc, client)

	connection := om.CurrMockedConnection
	connection.CleanHistory()

	// Scale down then
	sc = DefaultClusterBuilder().SetShardCountSpec(2).SetShardCountStatus(4).Build()
	_ = client.Update(context.TODO(), sc)

	checkReconcileSuccessful(t, reconciler, sc, client)

	// Two deployment modifications are expected
	connection.CheckOrderOfOperations(t, reflect.ValueOf(connection.ReadUpdateDeployment), reflect.ValueOf(connection.ReadUpdateDeployment))

	// todo ideally we need to check the "transitive" deployment that was created on first step, but let's check the
	// final version at least
	connection.CheckDeployment(t, createDeploymentFromShardedCluster(sc))

	// One shard has gone
	assert.Len(t, client.sets, 4)
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
	assert.Equal(t, ok(), updateOmDeploymentShardedCluster(mockOm, sc, newState, zap.S()))

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
	state := reconciler.buildKubeObjectsForShardedCluster(sc, defaultPodVars(), &ProjectConfig{}, zap.S())

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

func TestShardedCluster_WithTLSEnabled_AndX509Enabled_Succeeds(t *testing.T) {
	sc := DefaultClusterBuilder().
		WithTLS().
		Build()

	manager := newMockedManager(sc)
	client := manager.client

	client.configMaps[objectKey("", om.TestGroupName)] = x509ConfigMap()

	// create the secret the agent certs will exist in
	client.secrets[objectKey("", util.AgentSecretName)] = &corev1.Secret{}

	// create pre-approved TLS csrs for the sharded cluster
	// TODO: why does this test pass with namespace="" but fails with namespace=TestNamespace?
	client.csrs[objectKey("", fmt.Sprintf("%s-mongos-0.%s", sc.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
	client.csrs[objectKey("", fmt.Sprintf("%s-mongos-1.%s", sc.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
	client.csrs[objectKey("", fmt.Sprintf("%s-mongos-2.%s", sc.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
	client.csrs[objectKey("", fmt.Sprintf("%s-mongos-3.%s", sc.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
	client.csrs[objectKey("", fmt.Sprintf("%s-config-0.%s", sc.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
	client.csrs[objectKey("", fmt.Sprintf("%s-config-1.%s", sc.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
	client.csrs[objectKey("", fmt.Sprintf("%s-config-2.%s", sc.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
	client.csrs[objectKey("", fmt.Sprintf("%s-0.%s", sc.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
	client.csrs[objectKey("", fmt.Sprintf("%s-1.%s", sc.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
	client.csrs[objectKey("", fmt.Sprintf("%s-2.%s", sc.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
	client.csrs[objectKey("", fmt.Sprintf("%s-0-0.%s", sc.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
	client.csrs[objectKey("", fmt.Sprintf("%s-0-1.%s", sc.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
	client.csrs[objectKey("", fmt.Sprintf("%s-0-2.%s", sc.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
	client.csrs[objectKey("", fmt.Sprintf("%s-1-0.%s", sc.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
	client.csrs[objectKey("", fmt.Sprintf("%s-1-1.%s", sc.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
	client.csrs[objectKey("", fmt.Sprintf("%s-1-2.%s", sc.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)

	reconciler := newShardedClusterReconciler(manager, om.NewEmptyMockedOmConnection)
	actualResult, err := reconciler.Reconcile(requestFromObject(sc))
	expectedResult, _ := success()

	assert.Equal(t, expectedResult, actualResult)
	assert.Nil(t, err)
}

func createDeploymentFromShardedCluster(updatable Updatable) om.Deployment {
	sh := updatable.(*v1.MongoDB)
	state := createStateFromResource(sh)
	mongosProcesses := createProcesses(
		state.mongosSetHelper.BuildStatefulSet(),
		om.ProcessTypeMongos,
		sh,
		zap.S(),
	)
	configRs := buildReplicaSetFromStatefulSet(state.configSrvSetHelper.BuildStatefulSet(), sh, zap.S())
	shards := make([]om.ReplicaSetWithProcesses, len(state.shardsSetsHelpers))
	for i, s := range state.shardsSetsHelpers {
		shards[i] = buildReplicaSetFromStatefulSet(s.BuildStatefulSet(), sh, zap.S())
	}

	d := om.NewDeployment()
	d.MergeShardedCluster(sh.Name, mongosProcesses, configRs, shards, false)
	d.AddMonitoringAndBackup(mongosProcesses[0].HostName(), zap.S())
	return d
}

// createStateFromResource creates the kube state for the sharded cluster. Note, that it uses the `Status` of cluster
// instead of `Spec` as it tries to reflect the CURRENT state
func createStateFromResource(updatable Updatable) ShardedClusterKubeState {
	sh := updatable.(*v1.MongoDB)
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
		Persistent: util.BooleanRef(false),
		ConnectionSpec: v1.ConnectionSpec{Project: TestProjectConfigMapName,
			Credentials: TestCredentialsSecretName},
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

func (b *ClusterBuilder) SetSecurity(security v1.Security) *ClusterBuilder {
	b.Spec.Security = &security
	return b
}

func (b *ClusterBuilder) WithTLS() *ClusterBuilder {
	if b.Spec.Security == nil || b.Spec.Security.TLSConfig == nil {
		return b.SetSecurity(v1.Security{TLSConfig: &v1.TLSConfig{Enabled: true}})
	}
	b.Spec.Security.TLSConfig.Enabled = true
	return b
}

func (b *ClusterBuilder) SetClusterAuth(auth string) *ClusterBuilder {
	b.Spec.Security.ClusterAuthMode = auth
	return b
}
func (b *ClusterBuilder) Build() *v1.MongoDB {
	b.Spec.ResourceType = v1.ShardedCluster
	b.InitDefaults()
	return b.MongoDB
}
