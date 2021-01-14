package operator

import (
	"context"
	"testing"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	"github.com/google/uuid"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om/backup"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/watch"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/construct"

	"k8s.io/apimachinery/pkg/api/errors"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"
	"k8s.io/apimachinery/pkg/types"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/controlledfeature"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/stretchr/testify/assert"

	"reflect"

	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	certsv1 "k8s.io/api/certificates/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
	sc := DefaultClusterBuilder().SetShardCountSpec(4).SetShardCountStatus(4).Build()
	reconciler, client := defaultClusterReconciler(sc)

	checkReconcileSuccessful(t, reconciler, sc, client)

	connection := om.CurrMockedConnection
	connection.CleanHistory()

	// Scale down then
	sc = DefaultClusterBuilder().
		SetShardCountSpec(3).
		SetShardCountStatus(4).
		Build()

	_ = client.Update(context.TODO(), sc)

	checkReconcileSuccessful(t, reconciler, sc, client)

	// Two deployment modifications are expected
	connection.CheckOrderOfOperations(t, reflect.ValueOf(connection.ReadUpdateDeployment), reflect.ValueOf(connection.ReadUpdateDeployment))

	// todo ideally we need to check the "transitive" deployment that was created on first step, but let's check the
	// final version at least

	// the updated deployment should reflect that of a ShardedCluster with one fewer member
	scWith3Members := DefaultClusterBuilder().SetShardCountStatus(3).SetShardCountSpec(3).Build()
	connection.CheckDeployment(t, createDeploymentFromShardedCluster(scWith3Members), "auth", "ssl")

	// No matter how many members we scale down by, we will only have one fewer each reconciliation
	assert.Len(t, client.GetMapForObject(&appsv1.StatefulSet{}), 5)
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
		reflect.ValueOf(omConn.GetHosts), reflect.ValueOf(omConn.RemoveHost))

}

// TestPrepareScaleDownShardedCluster tests the scale down operation for config servers and mongods per shard. It checks
// that all members that will be removed are marked as unvoted
func TestPrepareScaleDownShardedCluster_ConfigMongodsUp(t *testing.T) {
	scBeforeScale := DefaultClusterBuilder().
		SetConfigServerCountStatus(3).
		SetConfigServerCountSpec(3).
		SetMongodsPerShardCountStatus(4).
		SetMongodsPerShardCountSpec(4).
		Build()

	r, _ := newShardedClusterReconcilerFromResource(*scBeforeScale, om.NewEmptyMockedOmConnection)

	oldDeployment := createDeploymentFromShardedCluster(scBeforeScale)
	mockedOmConnection := om.NewMockedOmConnection(oldDeployment)

	scAfterScale := DefaultClusterBuilder().
		SetConfigServerCountStatus(3).
		SetConfigServerCountSpec(2).
		SetMongodsPerShardCountStatus(4).
		SetMongodsPerShardCountSpec(3).
		Build()

	r.initCountsForThisReconciliation(*scAfterScale)

	assert.NoError(t, r.prepareScaleDownShardedCluster(mockedOmConnection, scBeforeScale, &env.PodEnvVars{}, "", zap.S()))

	// create the expected deployment from the sharded cluster that has not yet scaled
	// expected change of state: rs members are marked unvoted
	expectedDeployment := createDeploymentFromShardedCluster(scBeforeScale)
	firstConfig := scAfterScale.ConfigRsName() + "-2"
	firstShard := scAfterScale.ShardRsName(0) + "-3"
	secondShard := scAfterScale.ShardRsName(1) + "-3"

	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(scAfterScale.ConfigRsName(), []string{firstConfig}))
	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(scAfterScale.ShardRsName(0), []string{firstShard}))
	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(scAfterScale.ShardRsName(1), []string{secondShard}))

	mockedOmConnection.CheckNumberOfUpdateRequests(t, 1)
	mockedOmConnection.CheckDeployment(t, expectedDeployment)
	// we don't remove hosts from monitoring at this stage
	mockedOmConnection.CheckMonitoredHostsRemoved(t, []string{})
}

// TestPrepareScaleDownShardedCluster_ShardsUpMongodsDown checks the situation when shards count increases and mongods
// count per shard is decreased - scale down operation is expected to be called only for existing shards
func TestPrepareScaleDownShardedCluster_ShardsUpMongodsDown(t *testing.T) {
	scBeforeScale := DefaultClusterBuilder().
		SetShardCountStatus(4).
		SetShardCountSpec(4).
		SetMongodsPerShardCountStatus(4).
		SetMongodsPerShardCountSpec(4).
		Build()

	r, _ := newShardedClusterReconcilerFromResource(*scBeforeScale, om.NewEmptyMockedOmConnection)

	oldDeployment := createDeploymentFromShardedCluster(scBeforeScale)
	mockedOmConnection := om.NewMockedOmConnection(oldDeployment)

	scAfterScale := DefaultClusterBuilder().
		SetShardCountStatus(4).
		SetShardCountSpec(2).
		SetMongodsPerShardCountStatus(4).
		SetMongodsPerShardCountSpec(3).
		Build()

	r.initCountsForThisReconciliation(*scAfterScale)

	assert.NoError(t, r.prepareScaleDownShardedCluster(mockedOmConnection, scBeforeScale, &env.PodEnvVars{}, "", zap.S()))

	// expected change of state: rs members are marked unvoted only for two shards (old state)
	expectedDeployment := createDeploymentFromShardedCluster(scBeforeScale)
	firstShard := scBeforeScale.ShardRsName(0) + "-3"
	secondShard := scBeforeScale.ShardRsName(1) + "-3"
	thirdShard := scBeforeScale.ShardRsName(2) + "-3"
	fourthShard := scBeforeScale.ShardRsName(3) + "-3"

	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(scBeforeScale.ShardRsName(0), []string{firstShard}))
	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(scBeforeScale.ShardRsName(1), []string{secondShard}))
	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(scBeforeScale.ShardRsName(2), []string{thirdShard}))
	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(scBeforeScale.ShardRsName(3), []string{fourthShard}))

	mockedOmConnection.CheckNumberOfUpdateRequests(t, 1)
	mockedOmConnection.CheckDeployment(t, expectedDeployment)
	//we don't remove hosts from monitoring at this stage
	mockedOmConnection.CheckOperationsDidntHappen(t, reflect.ValueOf(mockedOmConnection.RemoveHost))
}

func TestConstructConfigSrv(t *testing.T) {
	sc := DefaultClusterBuilder().Build()

	assert.NotPanics(t, func() {
		construct.DatabaseStatefulSet(*sc, construct.ConfigServerOptions())
	})
}

// TestPrepareScaleDownShardedCluster_OnlyMongos checks that if only mongos processes are scaled down - then no preliminary
// actions are done
func TestPrepareScaleDownShardedCluster_OnlyMongos(t *testing.T) {
	sc := DefaultClusterBuilder().SetMongosCountStatus(4).SetMongosCountSpec(2).Build()
	r, _ := newShardedClusterReconcilerFromResource(*sc, om.NewEmptyMockedOmConnection)

	oldDeployment := createDeploymentFromShardedCluster(sc)
	mockedOmConnection := om.NewMockedOmConnection(oldDeployment)

	assert.NoError(t, r.prepareScaleDownShardedCluster(mockedOmConnection, sc, &env.PodEnvVars{}, "", zap.S()))

	mockedOmConnection.CheckNumberOfUpdateRequests(t, 0)
	mockedOmConnection.CheckDeployment(t, createDeploymentFromShardedCluster(sc))
	mockedOmConnection.CheckOperationsDidntHappen(t, reflect.ValueOf(mockedOmConnection.RemoveHost))
}

// TestUpdateOmDeploymentShardedCluster_HostsRemovedFromMonitoring verifies that if scale down operation was performed -
// hosts are removed
func TestUpdateOmDeploymentShardedCluster_HostsRemovedFromMonitoring(t *testing.T) {
	sc := DefaultClusterBuilder().
		SetMongosCountStatus(2).
		SetMongosCountSpec(2).
		SetConfigServerCountStatus(4).
		SetConfigServerCountSpec(4).
		Build()

	r, _ := newShardedClusterReconcilerFromResource(*sc, om.NewEmptyMockedOmConnection)

	// the deployment we create should have all processes
	mockOm := om.NewMockedOmConnection(createDeploymentFromShardedCluster(sc))

	// we need to create a different sharded cluster that is currently in the process of scaling down
	sc = DefaultClusterBuilder().
		SetMongosCountStatus(2).
		SetMongosCountSpec(1).
		SetConfigServerCountStatus(4).
		SetConfigServerCountSpec(3).
		Build()

	r.initCountsForThisReconciliation(*sc)

	// updateOmDeploymentShardedCluster checks an element from ac.Auth.DeploymentAuthMechanisms
	// so we need to ensure it has a non-nil value. An empty list implies no authentication
	_ = mockOm.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.DeploymentAuthMechanisms = []string{}
		return nil
	}, nil)

	assert.Equal(t, workflow.OK(), r.updateOmDeploymentShardedCluster(mockOm, sc, &env.PodEnvVars{}, "", zap.S()))

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadUpdateDeployment), reflect.ValueOf(mockOm.RemoveHost))

	// expected change of state: no unvoting - just monitoring deleted
	firstConfig := sc.ConfigRsName() + "-3"
	firstMongos := sc.MongosRsName() + "-1"

	mockOm.CheckMonitoredHostsRemoved(t, []string{
		firstConfig + ".slaney-cs.mongodb.svc.cluster.local",
		firstMongos + ".slaney-svc.mongodb.svc.cluster.local",
	})
}

// CLOUDP-32765: checks that pod anti affinity rule spreads mongods inside one shard, not inside all shards
func TestPodAntiaffinity_MongodsInsideShardAreSpread(t *testing.T) {
	sc := DefaultClusterBuilder().Build()

	firstShardSet, err := construct.DatabaseStatefulSet(*sc, construct.ShardOptions(0))
	assert.NoError(t, err)
	secondShardSet, err := construct.DatabaseStatefulSet(*sc, construct.ShardOptions(1))
	assert.NoError(t, err)

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
		EnableX509().
		Build()

	reconciler, client := defaultClusterReconciler(sc)

	cMap := configMap()
	createAgentCSRs(1, client, certsv1.CertificateApproved)
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
	expectedResult := reconcile.Result{}

	assert.Equal(t, expectedResult, actualResult)
	assert.Nil(t, err)
}

func addCsrs(client kubernetesClient.Client, csrs ...certsv1.CertificateSigningRequest) {
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
	expectedResult := reconcile.Result{}

	assert.Equal(t, expectedResult, actualResult)
	assert.Nil(t, err)

	allConfigs := reconciler.getAllConfigs(*sc, &env.PodEnvVars{}, "", zap.S())

	assert.False(t, anyStatefulSetNeedsToPublishState(*sc, client, allConfigs, zap.S()))

	// attempting to set tls to false
	sc.Spec.Security.TLSConfig.Enabled = false

	// Ops Manager state needs to be published first as we want to reach goal state before unmounting certificates
	allConfigs = reconciler.getAllConfigs(*sc, &env.PodEnvVars{}, "", zap.S())
	assert.True(t, anyStatefulSetNeedsToPublishState(*sc, client, allConfigs, zap.S()))
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

func TestFeatureControlsNoAuth(t *testing.T) {
	sc := DefaultClusterBuilder().RemoveAuth().Build()
	reconciler, client := defaultClusterReconciler(sc)
	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		context.Version = versionutil.OpsManagerVersion{
			VersionString: "4.2.2",
		}
		conn := om.NewEmptyMockedOmConnection(context)
		return conn
	}

	checkReconcileSuccessful(t, reconciler, sc, client)

	mockedConn := om.CurrMockedConnection
	cf, _ := mockedConn.GetControlledFeature()

	assert.Len(t, cf.Policies, 1)

	assert.Equal(t, cf.ManagementSystem.Version, util.OperatorVersion)
	assert.Equal(t, cf.ManagementSystem.Name, util.OperatorName)
	assert.Equal(t, cf.Policies[0].PolicyType, controlledfeature.ExternallyManaged)
	assert.Len(t, cf.Policies[0].DisabledParams, 0)

}

func TestScalingShardedCluster_ScalesOneMemberAtATime_WhenScalingUp(t *testing.T) {
	sc := DefaultClusterBuilder().
		SetMongodsPerShardCountSpec(3).
		SetMongodsPerShardCountStatus(3).
		SetConfigServerCountSpec(1).
		SetConfigServerCountStatus(1).
		SetMongosCountSpec(1).
		SetMongosCountStatus(0).
		SetShardCountSpec(1).
		SetShardCountStatus(0).
		Build()

	reconciler, client := defaultClusterReconciler(sc)
	// perform initial reconciliation so we are not creating a new resource
	checkReconcileSuccessful(t, reconciler, sc, client)

	// Scale up the Sharded Cluster
	sc.Spec.MongodsPerShardCount = 6
	sc.Spec.MongosCount = 3
	sc.Spec.ShardCount = 2
	sc.Spec.ConfigServerCount = 2

	err := client.Update(context.TODO(), sc)
	assert.NoError(t, err)

	var deployment om.Deployment
	performReconciliation := func(shouldRetry bool) {
		res, err := reconciler.Reconcile(requestFromObject(sc))
		assert.NoError(t, err)
		if shouldRetry {
			assert.Equal(t, time.Duration(10000000000), res.RequeueAfter)
		} else {
			assert.Equal(t, time.Duration(0), res.RequeueAfter)
		}
		err = client.Get(context.TODO(), sc.ObjectKey(), sc)
		assert.NoError(t, err)

		deployment, err = om.CurrMockedConnection.ReadDeployment()
		assert.NoError(t, err)
	}

	getShard := func(i int) appsv1.StatefulSet {
		sts := appsv1.StatefulSet{}
		err := client.Get(context.TODO(), types.NamespacedName{Name: sc.ShardRsName(i), Namespace: sc.Namespace}, &sts)
		assert.NoError(t, err)
		return sts
	}

	t.Run("1st reconciliation", func(t *testing.T) {
		performReconciliation(true)

		assert.Equal(t, 2, sc.Status.MongosCount)
		assert.Equal(t, 2, sc.Status.ConfigServerCount)
		assert.Equal(t, int32(4), *getShard(0).Spec.Replicas)
		assert.Equal(t, int32(4), *getShard(1).Spec.Replicas)
		assert.Len(t, deployment.GetAllProcessNames(), 12)
	})

	t.Run("2nd reconciliation", func(t *testing.T) {
		performReconciliation(true)
		assert.Equal(t, 3, sc.Status.MongosCount)
		assert.Equal(t, 2, sc.Status.ConfigServerCount)
		assert.Equal(t, int32(5), *getShard(0).Spec.Replicas)
		assert.Equal(t, int32(5), *getShard(1).Spec.Replicas)
		assert.Len(t, deployment.GetAllProcessNames(), 15)
	})

	t.Run("3rd reconciliation", func(t *testing.T) {
		performReconciliation(false)
		assert.Equal(t, 3, sc.Status.MongosCount)
		assert.Equal(t, 2, sc.Status.ConfigServerCount)
		assert.Equal(t, int32(6), *getShard(0).Spec.Replicas)
		assert.Equal(t, int32(6), *getShard(1).Spec.Replicas)
		assert.Len(t, deployment.GetAllProcessNames(), 17)
	})
}

func TestScalingShardedCluster_ScalesOneMemberAtATime_WhenScalingDown(t *testing.T) {
	sc := DefaultClusterBuilder().
		SetMongodsPerShardCountSpec(6).
		SetMongodsPerShardCountStatus(6).
		SetConfigServerCountSpec(3).
		SetConfigServerCountStatus(3).
		SetMongosCountSpec(3).
		SetMongosCountStatus(3).
		SetShardCountSpec(2).
		SetShardCountStatus(2).
		Build()

	reconciler, client := defaultClusterReconciler(sc)
	// perform initial reconciliation so we are not creating a new resource
	checkReconcileSuccessful(t, reconciler, sc, client)

	err := client.Get(context.TODO(), sc.ObjectKey(), sc)
	assert.NoError(t, err)

	assert.Equal(t, 2, sc.Status.ShardCount)

	// Scale up the Sharded Cluster
	sc.Spec.MongodsPerShardCount = 3
	sc.Spec.MongosCount = 1
	sc.Spec.ShardCount = 1
	sc.Spec.ConfigServerCount = 1

	err = client.Update(context.TODO(), sc)
	assert.NoError(t, err)

	performReconciliation := func(shouldRetry bool) {
		res, err := reconciler.Reconcile(requestFromObject(sc))
		assert.NoError(t, err)
		if shouldRetry {
			assert.Equal(t, time.Duration(10000000000), res.RequeueAfter)
		} else {
			assert.Equal(t, time.Duration(0), res.RequeueAfter)
		}
		err = client.Get(context.TODO(), sc.ObjectKey(), sc)
		assert.NoError(t, err)
	}

	getShard := func(i int) *appsv1.StatefulSet {
		sts := appsv1.StatefulSet{}
		err := client.Get(context.TODO(), types.NamespacedName{Name: sc.ShardRsName(i), Namespace: sc.Namespace}, &sts)
		if errors.IsNotFound(err) {
			return nil
		}
		return &sts
	}

	t.Run("1st reconciliation", func(t *testing.T) {
		performReconciliation(true)
		assert.Equal(t, 2, sc.Status.ShardCount)
		assert.Equal(t, 2, sc.Status.MongosCount)
		assert.Equal(t, 2, sc.Status.ConfigServerCount)
		assert.Equal(t, int32(5), *getShard(0).Spec.Replicas)
		assert.NotNil(t, getShard(1), "Shard should be removed until the scaling operation is complete")
	})
	t.Run("2nd reconciliation", func(t *testing.T) {
		performReconciliation(true)
		assert.Equal(t, 2, sc.Status.ShardCount)
		assert.Equal(t, 1, sc.Status.MongosCount)
		assert.Equal(t, 1, sc.Status.ConfigServerCount)
		assert.Equal(t, int32(4), *getShard(0).Spec.Replicas)
		assert.NotNil(t, getShard(1), "Shard should be removed until the scaling operation is complete")
	})
	t.Run("Final reconciliation", func(t *testing.T) {
		performReconciliation(false)
		assert.Equal(t, 1, sc.Status.ShardCount, "Upon finishing reconciliation, the original shard count should be set to the current value")
		assert.Equal(t, 1, sc.Status.MongosCount)
		assert.Equal(t, 1, sc.Status.ConfigServerCount)
		assert.Equal(t, int32(3), *getShard(0).Spec.Replicas)
		assert.Nil(t, getShard(1), "Shard should be removed as we have reached have finished scaling")
	})
}

func TestFeatureControlsAuthEnabled(t *testing.T) {
	sc := DefaultClusterBuilder().Build()
	reconciler, client := defaultClusterReconciler(sc)
	reconciler.omConnectionFactory = func(context *om.OMContext) om.Connection {
		context.Version = versionutil.OpsManagerVersion{
			VersionString: "4.2.2",
		}
		conn := om.NewEmptyMockedOmConnection(context)
		return conn
	}

	checkReconcileSuccessful(t, reconciler, sc, client)

	mockedConn := om.CurrMockedConnection
	cf, _ := mockedConn.GetControlledFeature()

	assert.Len(t, cf.Policies, 2)

	assert.Equal(t, cf.ManagementSystem.Version, util.OperatorVersion)
	assert.Equal(t, cf.ManagementSystem.Name, util.OperatorName)

	var policies []controlledfeature.PolicyType
	for _, p := range cf.Policies {
		policies = append(policies, p.PolicyType)
	}

	assert.Contains(t, policies, controlledfeature.ExternallyManaged)
	assert.Contains(t, policies, controlledfeature.DisableAuthenticationMechanisms)
}

func TestShardedClusterPortsAreConfigurable_WithAdditionalMongoConfig(t *testing.T) {
	configSrvConfig := mdbv1.NewAdditionalMongodConfig("net.port", 30000)
	mongosConfig := mdbv1.NewAdditionalMongodConfig("net.port", 30001)
	shardConfig := mdbv1.NewAdditionalMongodConfig("net.port", 30002)

	sc := mdbv1.NewClusterBuilder().
		SetNamespace(mock.TestNamespace).
		SetConnectionSpec(testConnectionSpec()).
		SetConfigSrvAdditionalConfig(configSrvConfig).
		SetMongosAdditionalConfig(mongosConfig).
		SetShardAdditionalConfig(shardConfig).
		Build()

	reconciler, client := defaultClusterReconciler(sc)

	checkReconcileSuccessful(t, reconciler, sc, client)

	t.Run("Config Server Port is configured", func(t *testing.T) {
		configSrvSvc, err := client.GetService(kube.ObjectKey(sc.Namespace, sc.ConfigSrvServiceName()))
		assert.NoError(t, err)
		assert.Equal(t, int32(30000), configSrvSvc.Spec.Ports[0].Port)
	})

	t.Run("Mongos Port is configured", func(t *testing.T) {
		mongosSvc, err := client.GetService(kube.ObjectKey(sc.Namespace, sc.ServiceName()))
		assert.NoError(t, err)
		assert.Equal(t, int32(30001), mongosSvc.Spec.Ports[0].Port)
	})

	t.Run("Shard Port is configured", func(t *testing.T) {
		shardSvc, err := client.GetService(kube.ObjectKey(sc.Namespace, sc.ShardServiceName()))
		assert.NoError(t, err)
		assert.Equal(t, int32(30002), shardSvc.Spec.Ports[0].Port)
	})
}

func TestShardedClusterSettingDeprecatedFieldsAddsWarning(t *testing.T) {
	sc := mdbv1.NewClusterBuilder().
		SetNamespace(mock.TestNamespace).
		SetConnectionSpec(testConnectionSpec()).
		Build()
	reconciler, client := defaultClusterReconciler(sc)

	t.Run("Adding shortcut resources adds warnings", func(t *testing.T) {
		err := client.Get(context.TODO(), types.NamespacedName{Name: sc.Name, Namespace: sc.Namespace}, sc)
		assert.NoError(t, err)

		sc.Spec.ConfigSrvPodSpec.Cpu = "1"

		err = client.Update(context.TODO(), sc)
		assert.NoError(t, err)

		checkReconcileSuccessful(t, reconciler, sc, client)

		assert.NotEmpty(t, sc.Status.Warnings)
		assert.Subset(t, sc.Status.Warnings, []status.Warning{mdbv1.UseOfDeprecatedShortcutFieldsWarning})
	})

	t.Run("No shortcut resources won't get warnings", func(t *testing.T) {
		err := client.Get(context.TODO(), types.NamespacedName{Name: sc.Name, Namespace: sc.Namespace}, sc)
		assert.NoError(t, err)

		sc.Spec.ConfigSrvPodSpec.Cpu = ""

		err = client.Update(context.TODO(), sc)
		assert.NoError(t, err)

		checkReconcileSuccessful(t, reconciler, sc, client)

		assert.Empty(t, sc.Status.Warnings)
	})

	t.Run("Multiple shortcut resources adds only 1 warning", func(t *testing.T) {
		err := client.Get(context.TODO(), types.NamespacedName{Name: sc.Name, Namespace: sc.Namespace}, sc)
		assert.NoError(t, err)

		sc.Spec.ConfigSrvPodSpec.Cpu = "1"
		sc.Spec.MongosPodSpec.Memory = "1"
		sc.Spec.ShardPodSpec.MemoryRequests = "2"

		err = client.Update(context.TODO(), sc)
		assert.NoError(t, err)

		checkReconcileSuccessful(t, reconciler, sc, client)

		assert.NotEmpty(t, sc.Status.Warnings)
		assert.Subset(t, sc.Status.Warnings, []status.Warning{mdbv1.UseOfDeprecatedShortcutFieldsWarning})
		assert.Len(t, sc.Status.Warnings, 1)
	})
}

//TestShardedCluster_ConfigMapAndSecretWatched verifies that config map and secret are added to the internal
//map that allows to watch them for changes
func TestShardedCluster_ConfigMapAndSecretWatched(t *testing.T) {
	sc := DefaultClusterBuilder().Build()

	reconciler, client := defaultClusterReconciler(sc)

	checkReconcileSuccessful(t, reconciler, sc, client)

	expected := map[watch.Object][]types.NamespacedName{
		{ResourceType: watch.ConfigMap, Resource: kube.ObjectKey(mock.TestNamespace, mock.TestProjectConfigMapName)}: {kube.ObjectKey(mock.TestNamespace, sc.Name)},
		{ResourceType: watch.Secret, Resource: kube.ObjectKey(mock.TestNamespace, sc.Spec.Credentials)}:              {kube.ObjectKey(mock.TestNamespace, sc.Name)},
	}

	assert.Equal(t, reconciler.WatchedResources, expected)
}

func TestBackupConfiguration_ShardedCluster(t *testing.T) {
	sc := mdbv1.NewClusterBuilder().
		SetNamespace(mock.TestNamespace).
		SetConnectionSpec(testConnectionSpec()).
		SetBackup(mdbv1.Backup{
			Mode: "enabled",
		}).
		Build()

	reconciler, client := defaultClusterReconciler(sc)

	clusterId := uuid.New().String()
	// configure backup for this project in Ops Manager in the mocked connection
	om.CurrMockedConnection = om.NewMockedOmConnection(om.NewDeployment())
	om.CurrMockedConnection.UpdateBackupConfig(&backup.Config{
		ClusterId: clusterId,
		Status:    backup.Inactive,
	})

	t.Run("Backup can be started", func(t *testing.T) {
		checkReconcileSuccessful(t, reconciler, sc, client)

		configResponse, _ := om.CurrMockedConnection.ReadBackupConfigs()
		assert.Len(t, configResponse.Configs, 1)

		config := configResponse.Configs[0]

		assert.Equal(t, backup.Started, config.Status)
		assert.Equal(t, clusterId, config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
	})

	t.Run("Backup can be stopped", func(t *testing.T) {
		sc.Spec.Backup.Mode = "disabled"
		err := client.Update(context.TODO(), sc)
		assert.NoError(t, err)

		checkReconcileSuccessful(t, reconciler, sc, client)

		configResponse, _ := om.CurrMockedConnection.ReadBackupConfigs()
		assert.Len(t, configResponse.Configs, 1)

		config := configResponse.Configs[0]

		assert.Equal(t, backup.Stopped, config.Status)
		assert.Equal(t, clusterId, config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
	})

	t.Run("Backup can be terminated", func(t *testing.T) {
		sc.Spec.Backup.Mode = "terminated"
		err := client.Update(context.TODO(), sc)
		assert.NoError(t, err)

		checkReconcileSuccessful(t, reconciler, sc, client)

		configResponse, _ := om.CurrMockedConnection.ReadBackupConfigs()
		assert.Len(t, configResponse.Configs, 1)

		config := configResponse.Configs[0]

		assert.Equal(t, backup.Terminating, config.Status)
		assert.Equal(t, clusterId, config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
	})

}

func assertPodSpecSts(t *testing.T, sts *appsv1.StatefulSet) {
	assertPodSpecTemplate(t, "some-node-name", "some-host-name", corev1.RestartPolicyAlways, sts)
}

func assertMongosSts(t *testing.T, sts *appsv1.StatefulSet) {
	assertPodSpecTemplate(t, "some-node-name-mongos", "some-host-name-mongos", corev1.RestartPolicyNever, sts)
}

func assertConfigSvrSts(t *testing.T, sts *appsv1.StatefulSet) {
	assertPodSpecTemplate(t, "some-node-name-config", "some-host-name-config", corev1.RestartPolicyOnFailure, sts)
}

func assertPodSpecTemplate(t *testing.T, nodeName, hostName string, restartPolicy corev1.RestartPolicy, sts *appsv1.StatefulSet) {
	podSpecTemplate := sts.Spec.Template.Spec
	// ensure values were passed to the stateful set
	assert.Equal(t, nodeName, podSpecTemplate.NodeName)
	assert.Equal(t, hostName, podSpecTemplate.Hostname)
	assert.Equal(t, restartPolicy, podSpecTemplate.RestartPolicy)

	assert.Equal(t, util.DatabaseContainerName, podSpecTemplate.Containers[0].Name, "Database container should always be first")
	assert.True(t, volumeMountWithNameExists(podSpecTemplate.Containers[0].VolumeMounts, construct.PvcNameDatabaseScripts))
}

func createDeploymentFromShardedCluster(updatable v1.CustomResourceReadWriter) om.Deployment {
	sh := updatable.(*mdbv1.MongoDB)

	state := createStateFromResourceStatus(sh)

	mongosSts, _ := construct.DatabaseStatefulSet(*sh, construct.MongosOptions(Replicas(sh.Spec.MongosCount)))
	mongosProcesses := createMongosProcesses(mongosSts, sh)
	configSvrSts, _ := construct.DatabaseStatefulSet(*sh, construct.ConfigServerOptions(Replicas(sh.Spec.ConfigServerCount)))

	configRs := buildReplicaSetFromProcesses(configSvrSts.Name, createConfigSrvProcesses(configSvrSts, sh), sh)
	shards := make([]om.ReplicaSetWithProcesses, len(state.shardsSetsHelpers))
	for i := range state.shardsSetsHelpers {
		shardSts, _ := construct.DatabaseStatefulSet(*sh, construct.ShardOptions(i, Replicas(sh.Spec.MongodsPerShardCount)))
		shards[i] = buildReplicaSetFromProcesses(shardSts.Name, createShardProcesses(shardSts, sh), sh)
	}

	d := om.NewDeployment()
	d.MergeShardedCluster(sh.Name, mongosProcesses, configRs, shards, false)
	d.AddMonitoringAndBackup(zap.S(), sh.Spec.GetTLSConfig().IsEnabled())
	return d
}

// createStateFromResource creates the kube state for the sharded cluster. Note, that it uses the `Status` of cluster
// instead of `Spec` as it tries to reflect the CURRENT state
func createStateFromResourceStatus(updatable v1.CustomResourceReadWriter) ShardedClusterKubeState {
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
	r, manager := newShardedClusterReconcilerFromResource(*sc, om.NewEmptyMockedOmConnection)
	manager.Client.AddDefaultMdbConfigResources()
	return r, manager.Client
}

func newShardedClusterReconcilerFromResource(sc mdbv1.MongoDB, omFunc om.ConnectionFactory) (*ReconcileMongoDbShardedCluster, *mock.MockedManager) {
	mgr := mock.NewManager(&sc)
	r := &ReconcileMongoDbShardedCluster{
		ReconcileCommonController: newReconcileCommonController(mgr),
		ResourceWatcher:           watch.NewResourceWatcher(),
		omConnectionFactory:       omFunc,
	}
	r.initCountsForThisReconciliation(sc)
	return r, mgr
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
		ShardedClusterSpec: mdbv1.ShardedClusterSpec{
			ConfigSrvSpec: &mdbv1.ShardedClusterComponentSpec{},
			MongosSpec:    &mdbv1.ShardedClusterComponentSpec{},
			ShardSpec:     &mdbv1.ShardedClusterComponentSpec{},
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

func (b *ClusterBuilder) EnableSCRAM() *ClusterBuilder {
	b.Spec.Security.Authentication.Enabled = true
	b.Spec.Security.Authentication.Modes = append(b.Spec.Security.Authentication.Modes, util.SCRAM)
	return b
}

func (b *ClusterBuilder) SetClusterAuth(auth string) *ClusterBuilder {
	b.Spec.Security.ClusterAuthMode = auth
	return b
}

func (b *ClusterBuilder) RemoveAuth() *ClusterBuilder {
	b.Spec.Security.Authentication = nil

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

func configMap() corev1.ConfigMap {
	return configmap.Builder().
		SetName(om.TestGroupName).
		SetNamespace(mock.TestNamespace).
		SetField(util.OmBaseUrl, om.TestURL).
		SetField(util.OmProjectName, om.TestGroupName).
		Build()
}
