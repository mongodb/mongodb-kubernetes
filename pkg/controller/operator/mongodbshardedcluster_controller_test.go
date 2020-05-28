package operator

import (
	"context"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"

	"github.com/stretchr/testify/assert"

	"reflect"

	"os"

	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	certsv1 "k8s.io/api/certificates/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestShardedClusterEventMethodsHandlePanic(t *testing.T) {
	// restoring
	defer InitDefaultEnvVariables()

	// nullifying env variable will result in panic exception raised
	os.Setenv(util.AutomationAgentImage, "")
	sc := DefaultClusterBuilder().Build()

	reconciler, client := defaultClusterReconciler(sc)
	checkReconcileFailed(t,
		reconciler,
		sc,
		true,
		"Failed to reconcile Sharded Cluster: MONGODB_ENTERPRISE_DATABASE_IMAGE environment variable is not set!",
		client,
	)

}

func TestReconcileCreateShardedCluster(t *testing.T) {
	sc := DefaultClusterBuilder().Build()

	reconciler, client := defaultClusterReconciler(sc)

	checkReconcileSuccessful(t, reconciler, sc, client)

	assert.Len(t, client.GetMapForObject(&corev1.Secret{}), 2)
	assert.Len(t, client.GetMapForObject(&corev1.Service{}), 3)
	assert.Len(t, client.GetMapForObject(&appsv1.StatefulSet{}), 4)
	assert.Equal(t, *client.GetSet(objectKey(sc.Namespace, sc.ConfigRsName())).Spec.Replicas, int32(sc.Spec.ConfigServerCount))
	assert.Equal(t, *client.GetSet(objectKey(sc.Namespace, sc.MongosRsName())).Spec.Replicas, int32(sc.Spec.MongosCount))
	assert.Equal(t, *client.GetSet(objectKey(sc.Namespace, sc.ShardRsName(0))).Spec.Replicas, int32(sc.Spec.MongodsPerShardCount))
	assert.Equal(t, *client.GetSet(objectKey(sc.Namespace, sc.ShardRsName(1))).Spec.Replicas, int32(sc.Spec.MongodsPerShardCount))

	connection := om.CurrMockedConnection
	connection.CheckDeployment(t, createDeploymentFromShardedCluster(sc), "auth", "ssl")
	connection.CheckNumberOfUpdateRequests(t, 1)
	// we don't remove hosts from monitoring if there is no scale down
	connection.CheckOperationsDidntHappen(t, reflect.ValueOf(connection.GetHosts), reflect.ValueOf(connection.RemoveHost))
}

func TestReconcileCreateShardedCluster_ScaleDown(t *testing.T) {
	// First creation
	sc := DefaultClusterBuilder().SetShardCountSpec(4).Build()

	reconciler, client := defaultClusterReconciler(sc)

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
	connection.CheckDeployment(t, createDeploymentFromShardedCluster(sc), "auth", "ssl")

	// One shard has gone
	assert.Len(t, client.GetMapForObject(&appsv1.StatefulSet{}), 4)
}

// TestAddDeleteShardedCluster checks that no state is left in OpsManager on removal of the sharded cluster
func TestAddDeleteShardedCluster(t *testing.T) {
	// First we need to create a sharded cluster
	sc := DefaultClusterBuilder().Build()

	reconciler, client := defaultClusterReconciler(sc)
	reconciler.omConnectionFactory = om.NewEmptyMockedOmConnectionWithDelay

	checkReconcileSuccessful(t, reconciler, sc, client)
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

	// updateOmDeploymentShardedCluster checks an element from ac.Auth.DeploymentAuthMechanisms
	// so we need to ensure it has a non-nil value. An empty list implies no authentication
	_ = mockOm.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.DeploymentAuthMechanisms = []string{}
		return nil
	}, nil)

	r := newShardedClusterReconciler(mock.NewManager(sc), om.NewEmptyMockedOmConnection)
	assert.Equal(t, workflow.OK(), r.updateOmDeploymentShardedCluster(mockOm, sc, newState, zap.S()))

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

	reconciler := newShardedClusterReconciler(mock.NewManager(sc), om.NewEmptyMockedOmConnection)
	state := reconciler.buildKubeObjectsForShardedCluster(sc, defaultPodVars(), mdbv1.ProjectConfig{}, zap.S())

	shardHelpers := state.shardsSetsHelpers

	assert.Len(t, shardHelpers, 2)

	firstShardSet, _ := shardHelpers[0].BuildStatefulSet()
	secondShardSet, _ := shardHelpers[1].BuildStatefulSet()

	assert.Equal(t, sc.ShardRsName(0), firstShardSet.Spec.Selector.MatchLabels[PodAntiAffinityLabelKey])
	assert.Equal(t, sc.ShardRsName(1), secondShardSet.Spec.Selector.MatchLabels[PodAntiAffinityLabelKey])

	firstShartPodAffinityTerm := firstShardSet.Spec.Template.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm
	assert.Equal(t, firstShartPodAffinityTerm.LabelSelector.MatchLabels[PodAntiAffinityLabelKey], sc.ShardRsName(0))

	secondShartPodAffinityTerm := secondShardSet.Spec.Template.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm
	assert.Equal(t, secondShartPodAffinityTerm.LabelSelector.MatchLabels[PodAntiAffinityLabelKey], sc.ShardRsName(1))
}

func TestShardedCluster_WithTLSEnabled_AndX509Enabled_Succeeds(t *testing.T) {
	sc := DefaultClusterBuilder().
		EnableTLS().
		Build()

	reconciler, client := defaultClusterReconciler(sc)

	cMap := x509ConfigMap()
	client.GetMapForObject(&corev1.ConfigMap{})[objectKey("", om.TestGroupName)] = &cMap

	// create the secret the agent certs will exist in
	client.GetMapForObject(&corev1.Secret{})[objectKey("", util.AgentSecretName)] = &corev1.Secret{}

	// create pre-approved TLS csrs for the sharded cluster
	addCsrs(client,
		createCSR(fmt.Sprintf("%s-mongos-0", sc.Name), mock.TestNamespace, certsv1.CertificateApproved),
		createCSR(fmt.Sprintf("%s-mongos-1", sc.Name), mock.TestNamespace, certsv1.CertificateApproved),
		createCSR(fmt.Sprintf("%s-mongos-2", sc.Name), mock.TestNamespace, certsv1.CertificateApproved),
		createCSR(fmt.Sprintf("%s-mongos-3", sc.Name), mock.TestNamespace, certsv1.CertificateApproved),
		createCSR(fmt.Sprintf("%s-config-0", sc.Name), mock.TestNamespace, certsv1.CertificateApproved),
		createCSR(fmt.Sprintf("%s-config-1", sc.Name), mock.TestNamespace, certsv1.CertificateApproved),
		createCSR(fmt.Sprintf("%s-config-2", sc.Name), mock.TestNamespace, certsv1.CertificateApproved),
		createCSR(fmt.Sprintf("%s-0", sc.Name), mock.TestNamespace, certsv1.CertificateApproved),
		createCSR(fmt.Sprintf("%s-1", sc.Name), mock.TestNamespace, certsv1.CertificateApproved),
		createCSR(fmt.Sprintf("%s-2", sc.Name), mock.TestNamespace, certsv1.CertificateApproved),
		createCSR(fmt.Sprintf("%s-0-0", sc.Name), mock.TestNamespace, certsv1.CertificateApproved),
		createCSR(fmt.Sprintf("%s-0-1", sc.Name), mock.TestNamespace, certsv1.CertificateApproved),
		createCSR(fmt.Sprintf("%s-0-2", sc.Name), mock.TestNamespace, certsv1.CertificateApproved),
		createCSR(fmt.Sprintf("%s-1-0", sc.Name), mock.TestNamespace, certsv1.CertificateApproved),
		createCSR(fmt.Sprintf("%s-1-1", sc.Name), mock.TestNamespace, certsv1.CertificateApproved),
		createCSR(fmt.Sprintf("%s-1-2", sc.Name), mock.TestNamespace, certsv1.CertificateApproved),
	)

	actualResult, err := reconciler.Reconcile(requestFromObject(sc))
	expectedResult, _ := success()

	assert.Equal(t, expectedResult, actualResult)
	assert.Nil(t, err)
}

func addCsrs(client *mock.MockedClient, csrs ...certsv1.CertificateSigningRequest) {
	for _, csr := range csrs {
		_ = client.Update(context.TODO(), &csr)
	}
}

func TestShardedCluster_NeedToPublishState(t *testing.T) {
	sc := DefaultClusterBuilder().
		EnableTLS().
		Build()

	// perform successful reconciliation to populate all the stateful sets in the mocked client
	reconciler, client := defaultClusterReconciler(sc)
	addKubernetesTlsResources(client, sc)
	actualResult, err := reconciler.Reconcile(requestFromObject(sc))
	expectedResult, _ := success()

	assert.Equal(t, expectedResult, actualResult)
	assert.Nil(t, err)

	kubeState := reconciler.buildKubeObjectsForShardedCluster(sc, defaultPodVars(), mdbv1.ProjectConfig{}, zap.S())
	assert.False(t, anyStatefulSetHelperNeedsToPublishState(kubeState, zap.S()))

	// attempting to set tls to false
	sc.Spec.Security.TLSConfig.Enabled = false

	// Ops Manager state needs to be published first as we want to reach goal state before unmounting certificates
	kubeState = reconciler.buildKubeObjectsForShardedCluster(sc, defaultPodVars(), mdbv1.ProjectConfig{}, zap.S())
	assert.True(t, anyStatefulSetHelperNeedsToPublishState(kubeState, zap.S()))
}

func TestShardedCustomPodSpecTemplate(t *testing.T) {
	sc := DefaultClusterBuilder().SetName("pod-spec-sc").EnableTLS().
		SetShardPodSpec(corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{
				NodeName: "some-node-name",
				Hostname: "some-host-name",
				Containers: []corev1.Container{{
					Name:  "my-custom-container-sc",
					Image: "my-custom-image",
					VolumeMounts: []corev1.VolumeMount{{
						Name: "my-volume-mount",
					}},
				}},
				RestartPolicy: corev1.RestartPolicyAlways,
			},
		}).SetMongosPodSpecTemplate(corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			NodeName: "some-node-name-mongos",
			Hostname: "some-host-name-mongos",
			Containers: []corev1.Container{{
				Name:  "my-custom-container-mongos",
				Image: "my-custom-image",
				VolumeMounts: []corev1.VolumeMount{{
					Name: "my-volume-mount",
				}},
			}},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}).SetPodConfigSvrSpecTemplate(corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			NodeName: "some-node-name-config",
			Hostname: "some-host-name-config",
			Containers: []corev1.Container{{
				Name:  "my-custom-container-config",
				Image: "my-custom-image",
				VolumeMounts: []corev1.VolumeMount{{
					Name: "my-volume-mount",
				}},
			}},
			RestartPolicy: corev1.RestartPolicyOnFailure,
		},
	}).Build()

	reconciler, client := defaultClusterReconciler(sc)

	addKubernetesTlsResources(client, sc)

	checkReconcileSuccessful(t, reconciler, sc, client)

	// read the stateful sets that were created by the operator
	statefulSetSc0 := getStatefulSet(client, objectKey(mock.TestNamespace, "pod-spec-sc-0"))
	statefulSetSc1 := getStatefulSet(client, objectKey(mock.TestNamespace, "pod-spec-sc-1"))
	statefulSetScConfig := getStatefulSet(client, objectKey(mock.TestNamespace, "pod-spec-sc-config"))
	statefulSetMongoS := getStatefulSet(client, objectKey(mock.TestNamespace, "pod-spec-sc-mongos"))
	assertPodSpecSts(t, statefulSetSc0)
	assertPodSpecSts(t, statefulSetSc1)
	assertMongosSts(t, statefulSetMongoS)
	assertConfigSvrSts(t, statefulSetScConfig)

	podSpecTemplateSc0 := statefulSetSc0.Spec.Template.Spec
	assert.Len(t, podSpecTemplateSc0.Containers, 2, "Should have 2 containers now")
	assert.Equal(t, util.DatabaseContainerName, podSpecTemplateSc0.Containers[0].Name, "Database container should always be first")
	assert.Equal(t, "my-custom-container-sc", podSpecTemplateSc0.Containers[1].Name, "Custom container should be second")

	podSpecTemplateSc1 := statefulSetSc1.Spec.Template.Spec
	assert.Len(t, podSpecTemplateSc1.Containers, 2, "Should have 2 containers now")
	assert.Equal(t, util.DatabaseContainerName, podSpecTemplateSc1.Containers[0].Name, "Database container should always be first")
	assert.Equal(t, "my-custom-container-sc", podSpecTemplateSc1.Containers[1].Name, "Custom container should be second")

	podSpecTemplateMongoS := statefulSetMongoS.Spec.Template.Spec
	assert.Len(t, podSpecTemplateMongoS.Containers, 2, "Should have 2 containers now")
	assert.Equal(t, util.DatabaseContainerName, podSpecTemplateMongoS.Containers[0].Name, "Database container should always be first")
	assert.Equal(t, "my-custom-container-mongos", podSpecTemplateMongoS.Containers[1].Name, "Custom container should be second")

	podSpecTemplateScConfig := statefulSetScConfig.Spec.Template.Spec
	assert.Len(t, podSpecTemplateScConfig.Containers, 2, "Should have 2 containers now")
	assert.Equal(t, util.DatabaseContainerName, podSpecTemplateScConfig.Containers[0].Name, "Database container should always be first")
	assert.Equal(t, "my-custom-container-config", podSpecTemplateScConfig.Containers[1].Name, "Custom container should be second")
}

func assertPodSpecSts(t *testing.T, sts *appsv1.StatefulSet) {
	assertPodSpecTemplate(t, "some-node-name", "some-host-name", util.SecretVolumeName, corev1.RestartPolicyAlways, sts)
}

func assertMongosSts(t *testing.T, sts *appsv1.StatefulSet) {
	assertPodSpecTemplate(t, "some-node-name-mongos", "some-host-name-mongos", util.SecretVolumeName, corev1.RestartPolicyNever, sts)
}

func assertConfigSvrSts(t *testing.T, sts *appsv1.StatefulSet) {
	assertPodSpecTemplate(t, "some-node-name-config", "some-host-name-config", util.SecretVolumeName, corev1.RestartPolicyOnFailure, sts)
}

func assertPodSpecTemplate(t *testing.T, nodeName, hostName, volumeName string, restartPolicy corev1.RestartPolicy, sts *appsv1.StatefulSet) {
	podSpecTemplate := sts.Spec.Template.Spec
	// ensure values were passed to the stateful set
	assert.Equal(t, nodeName, podSpecTemplate.NodeName)
	assert.Equal(t, hostName, podSpecTemplate.Hostname)
	assert.Equal(t, restartPolicy, podSpecTemplate.RestartPolicy)

	assert.Equal(t, util.DatabaseContainerName, podSpecTemplate.Containers[0].Name, "Database container should always be first")
	assert.Equal(t, volumeName, podSpecTemplate.Containers[0].VolumeMounts[0].Name, "Operator mounted volume should be present, not custom volume")
}

func createDeploymentFromShardedCluster(updatable mdbv1.CustomResourceReadWriter) om.Deployment {
	sh := updatable.(*mdbv1.MongoDB)
	state := createStateFromResource(sh)
	mongosSts, _ := state.mongosSetHelper.BuildStatefulSet()
	mongosProcesses := createProcesses(
		mongosSts,
		om.ProcessTypeMongos,
		sh,
	)

	configSvrSts, _ := state.configSrvSetHelper.BuildStatefulSet()
	configRs := buildReplicaSetFromStatefulSet(configSvrSts, sh)
	shards := make([]om.ReplicaSetWithProcesses, len(state.shardsSetsHelpers))
	for i, s := range state.shardsSetsHelpers {
		shardSts, _ := s.BuildStatefulSet()
		shards[i] = buildReplicaSetFromStatefulSet(shardSts, sh)
	}

	d := om.NewDeployment()
	d.MergeShardedCluster(sh.Name, mongosProcesses, configRs, shards, false)
	d.AddMonitoringAndBackup(mongosProcesses[0].HostName(), zap.S())
	return d
}

// createStateFromResource creates the kube state for the sharded cluster. Note, that it uses the `Status` of cluster
// instead of `Spec` as it tries to reflect the CURRENT state
func createStateFromResource(updatable mdbv1.CustomResourceReadWriter) ShardedClusterKubeState {
	sh := updatable.(*mdbv1.MongoDB)
	shardHelpers := make([]*StatefulSetHelper, sh.Status.ShardCount)
	for i := 0; i < sh.Status.ShardCount; i++ {
		shardHelpers[i] = defaultSetHelper().SetName(sh.ShardRsName(i)).SetService(sh.ShardServiceName()).SetReplicas(sh.Status.MongodsPerShardCount)
	}
	return ShardedClusterKubeState{
		mongosSetHelper:    defaultSetHelper().SetName(sh.MongosRsName()).SetService(sh.ServiceName()).SetReplicas(sh.Status.MongosCount),
		configSrvSetHelper: defaultSetHelper().SetName(sh.ConfigRsName()).SetService(sh.ConfigSrvServiceName()).SetReplicas(sh.Status.ConfigServerCount),
		shardsSetsHelpers:  shardHelpers}
}

// defaultClusterReconciler is the sharded cluster reconciler used in unit test. It "adds" necessary
// additional K8s objects (connection config map and secrets) necessary for reconciliation
func defaultClusterReconciler(sc *mdbv1.MongoDB) (*ReconcileMongoDbShardedCluster, *mock.MockedClient) {
	manager := mock.NewManager(sc)
	manager.Client.AddDefaultMdbConfigResources()

	return newShardedClusterReconciler(manager, om.NewEmptyMockedOmConnection), manager.Client
}

type ClusterBuilder struct {
	*mdbv1.MongoDB
}

func DefaultClusterBuilder() *ClusterBuilder {
	sizeConfig := mdbv1.MongodbShardedClusterSizeConfig{
		ShardCount:           2,
		MongodsPerShardCount: 3,
		ConfigServerCount:    3,
		MongosCount:          4,
	}

	status := mdbv1.MongoDbStatus{
		MongodbShardedClusterSizeConfig: sizeConfig,
	}

	spec := mdbv1.MongoDbSpec{
		Persistent: util.BooleanRef(false),
		ConnectionSpec: mdbv1.ConnectionSpec{
			OpsManagerConfig: &mdbv1.PrivateCloudConfig{
				ConfigMapRef: mdbv1.ConfigMapRef{
					Name: mock.TestProjectConfigMapName,
				},
			},
			Credentials: mock.TestCredentialsSecretName,
		},
		Version:                         "3.6.4",
		ResourceType:                    mdbv1.ShardedCluster,
		MongodbShardedClusterSizeConfig: sizeConfig,
		Security: &mdbv1.Security{
			TLSConfig: &mdbv1.TLSConfig{},
			Authentication: &mdbv1.Authentication{
				Modes: []string{},
			},
		},
	}

	resource := &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "slaney", Namespace: mock.TestNamespace},
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

func (b *ClusterBuilder) SetSecurity(security mdbv1.Security) *ClusterBuilder {
	b.Spec.Security = &security
	return b
}

func (b *ClusterBuilder) EnableTLS() *ClusterBuilder {
	if b.Spec.Security == nil || b.Spec.Security.TLSConfig == nil {
		return b.SetSecurity(mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{Enabled: true}})
	}
	b.Spec.Security.TLSConfig.Enabled = true
	return b
}

func (b *ClusterBuilder) EnableX509() *ClusterBuilder {
	b.Spec.Security.Authentication.Enabled = true
	b.Spec.Security.Authentication.Modes = append(b.Spec.Security.Authentication.Modes, util.X509)
	return b
}

func (b *ClusterBuilder) SetClusterAuth(auth string) *ClusterBuilder {
	b.Spec.Security.ClusterAuthMode = auth
	return b
}

func (b *ClusterBuilder) EnableAuth() *ClusterBuilder {
	b.Spec.Security.Authentication.Enabled = true
	return b
}

func (b *ClusterBuilder) SetAuthModes(modes []string) *ClusterBuilder {
	b.Spec.Security.Authentication.Modes = modes
	return b
}

func (b *ClusterBuilder) EnableX509InternalClusterAuth() *ClusterBuilder {
	b.Spec.Security.Authentication.InternalCluster = util.X509
	return b
}

func (b *ClusterBuilder) SetShardPodSpec(spec corev1.PodTemplateSpec) *ClusterBuilder {
	if b.Spec.ShardPodSpec == nil {
		b.Spec.ShardPodSpec = &mdbv1.MongoDbPodSpec{}
	}
	b.Spec.ShardPodSpec.PodTemplate = &spec
	return b
}

func (b *ClusterBuilder) SetPodConfigSvrSpecTemplate(spec corev1.PodTemplateSpec) *ClusterBuilder {
	if b.Spec.ConfigSrvPodSpec == nil {
		b.Spec.ConfigSrvPodSpec = &mdbv1.MongoDbPodSpec{}
	}
	b.Spec.ConfigSrvPodSpec.PodTemplate = &spec
	return b
}

func (b *ClusterBuilder) SetMongosPodSpecTemplate(spec corev1.PodTemplateSpec) *ClusterBuilder {
	if b.Spec.MongosPodSpec == nil {
		b.Spec.MongosPodSpec = &mdbv1.MongoDbPodSpec{}
	}
	b.Spec.MongosPodSpec.PodTemplate = &spec
	return b
}

func (b *ClusterBuilder) Build() *mdbv1.MongoDB {
	b.Spec.ResourceType = mdbv1.ShardedCluster
	b.InitDefaults()
	return b.MongoDB
}
