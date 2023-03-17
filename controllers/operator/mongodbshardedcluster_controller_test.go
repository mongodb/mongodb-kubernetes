package operator

import (
	"context"
	"testing"
	"time"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/backup"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"

	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"

	"k8s.io/apimachinery/pkg/api/errors"

	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/configmap"
	"k8s.io/apimachinery/pkg/types"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/controlledfeature"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/stretchr/testify/assert"

	"reflect"

	"fmt"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
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
	assert.Equal(t, *client.GetSet(kube.ObjectKey(sc.Namespace, sc.ConfigRsName())).Spec.Replicas, int32(sc.Spec.ConfigServerCount))
	assert.Equal(t, *client.GetSet(kube.ObjectKey(sc.Namespace, sc.MongosRsName())).Spec.Replicas, int32(sc.Spec.MongosCount))
	assert.Equal(t, *client.GetSet(kube.ObjectKey(sc.Namespace, sc.ShardRsName(0))).Spec.Replicas, int32(sc.Spec.MongodsPerShardCount))
	assert.Equal(t, *client.GetSet(kube.ObjectKey(sc.Namespace, sc.ShardRsName(1))).Spec.Replicas, int32(sc.Spec.MongodsPerShardCount))

	connection := om.CurrMockedConnection
	connection.CheckDeployment(t, createDeploymentFromShardedCluster(sc), "auth", "tls")
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
	connection.CheckDeployment(t, createDeploymentFromShardedCluster(scWith3Members), "auth", "tls")

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
	assert.NoError(t, reconciler.OnDelete(sc, zap.S()))

	// Operator doesn't mutate K8s state, so we don't check its changes, only OM
	omConn.CheckResourcesDeleted(t)

	omConn.CheckOrderOfOperations(t,
		reflect.ValueOf(omConn.ReadUpdateDeployment), reflect.ValueOf(omConn.ReadAutomationStatus),
		reflect.ValueOf(omConn.GetHosts), reflect.ValueOf(omConn.RemoveHost))

}

func getEmptyDeploymentOptions() deploymentOptions {
	return deploymentOptions{
		podEnvVars:         &env.PodEnvVars{},
		certTLSType:        map[string]bool{},
		prometheusCertHash: "",
	}
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

	assert.NoError(t, r.prepareScaleDownShardedCluster(mockedOmConnection, scBeforeScale, getEmptyDeploymentOptions(), zap.S()))

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

	assert.NoError(t, r.prepareScaleDownShardedCluster(mockedOmConnection, scBeforeScale, getEmptyDeploymentOptions(), zap.S()))

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
		construct.DatabaseStatefulSet(*sc, construct.ConfigServerOptions(construct.GetPodEnvOptions()), nil)
	})
}

// TestPrepareScaleDownShardedCluster_OnlyMongos checks that if only mongos processes are scaled down - then no preliminary
// actions are done
func TestPrepareScaleDownShardedCluster_OnlyMongos(t *testing.T) {
	sc := DefaultClusterBuilder().SetMongosCountStatus(4).SetMongosCountSpec(2).Build()
	r, _ := newShardedClusterReconcilerFromResource(*sc, om.NewEmptyMockedOmConnection)

	oldDeployment := createDeploymentFromShardedCluster(sc)
	mockedOmConnection := om.NewMockedOmConnection(oldDeployment)

	assert.NoError(t, r.prepareScaleDownShardedCluster(mockedOmConnection, sc, getEmptyDeploymentOptions(), zap.S()))

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

	assert.Equal(t, workflow.OK(), r.updateOmDeploymentShardedCluster(mockOm, sc, deploymentOptions{podEnvVars: &env.PodEnvVars{ProjectID: "abcd"}}, zap.S()))

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

	firstShardSet := construct.DatabaseStatefulSet(*sc, construct.ShardOptions(0, construct.GetPodEnvOptions()), nil)
	secondShardSet := construct.DatabaseStatefulSet(*sc, construct.ShardOptions(1, construct.GetPodEnvOptions()), nil)

	assert.Equal(t, sc.ShardRsName(0), firstShardSet.Spec.Selector.MatchLabels[construct.PodAntiAffinityLabelKey])
	assert.Equal(t, sc.ShardRsName(1), secondShardSet.Spec.Selector.MatchLabels[construct.PodAntiAffinityLabelKey])

	firstShartPodAffinityTerm := firstShardSet.Spec.Template.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm
	assert.Equal(t, firstShartPodAffinityTerm.LabelSelector.MatchLabels[construct.PodAntiAffinityLabelKey], sc.ShardRsName(0))

	secondShartPodAffinityTerm := secondShardSet.Spec.Template.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm
	assert.Equal(t, secondShartPodAffinityTerm.LabelSelector.MatchLabels[construct.PodAntiAffinityLabelKey], sc.ShardRsName(1))
}

func TestShardedCluster_WithTLSEnabled_AndX509Enabled_Succeeds(t *testing.T) {
	sc := DefaultClusterBuilder().
		EnableTLS().
		EnableX509().
		SetTLSCA("custom-ca").
		Build()

	reconciler, client := defaultClusterReconciler(sc)
	addKubernetesTlsResources(client, sc)

	actualResult, err := reconciler.Reconcile(context.TODO(), requestFromObject(sc))
	expectedResult := reconcile.Result{}

	assert.Equal(t, expectedResult, actualResult)
	assert.Nil(t, err)
}

func TestShardedCluster_NeedToPublishState(t *testing.T) {
	sc := DefaultClusterBuilder().
		EnableTLS().
		SetTLSCA("custom-ca").
		Build()

	// perform successful reconciliation to populate all the stateful sets in the mocked client
	reconciler, client := defaultClusterReconciler(sc)
	addKubernetesTlsResources(client, sc)
	actualResult, err := reconciler.Reconcile(context.TODO(), requestFromObject(sc))
	expectedResult := reconcile.Result{}

	assert.Equal(t, expectedResult, actualResult)
	assert.Nil(t, err)

	allConfigs := reconciler.getAllConfigs(*sc, getEmptyDeploymentOptions(), zap.S())

	assert.False(t, anyStatefulSetNeedsToPublishState(*sc, client, allConfigs, zap.S()))

	// attempting to set tls to false
	sc.Spec.Security.TLSConfig.Enabled = false

	err = client.Update(context.TODO(), sc)
	assert.NoError(t, err)

	// Ops Manager state needs to be published first as we want to reach goal state before unmounting certificates
	allConfigs = reconciler.getAllConfigs(*sc, getEmptyDeploymentOptions(), zap.S())
	assert.True(t, anyStatefulSetNeedsToPublishState(*sc, client, allConfigs, zap.S()))
}

func TestShardedCustomPodSpecTemplate(t *testing.T) {
	shardPodSpec := corev1.PodSpec{
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
	}

	mongosPodSpec := corev1.PodSpec{
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
	}

	configSrvPodSpec := corev1.PodSpec{
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
	}

	sc := DefaultClusterBuilder().SetName("pod-spec-sc").EnableTLS().SetTLSCA("custom-ca").
		SetShardPodSpec(corev1.PodTemplateSpec{
			Spec: shardPodSpec,
		}).SetMongosPodSpecTemplate(corev1.PodTemplateSpec{
		Spec: mongosPodSpec,
	}).SetPodConfigSvrSpecTemplate(corev1.PodTemplateSpec{
		Spec: configSrvPodSpec,
	}).Build()

	reconciler, client := defaultClusterReconciler(sc)

	addKubernetesTlsResources(client, sc)

	checkReconcileSuccessful(t, reconciler, sc, client)

	// read the stateful sets that were created by the operator
	statefulSetSc0, err := client.GetStatefulSet(kube.ObjectKey(mock.TestNamespace, "pod-spec-sc-0"))
	assert.NoError(t, err)
	statefulSetSc1, err := client.GetStatefulSet(kube.ObjectKey(mock.TestNamespace, "pod-spec-sc-1"))
	assert.NoError(t, err)
	statefulSetScConfig, err := client.GetStatefulSet(kube.ObjectKey(mock.TestNamespace, "pod-spec-sc-config"))
	assert.NoError(t, err)
	statefulSetMongoS, err := client.GetStatefulSet(kube.ObjectKey(mock.TestNamespace, "pod-spec-sc-mongos"))
	assert.NoError(t, err)

	// assert Pod Spec for Sharded cluster
	assertPodSpecSts(t, &statefulSetSc0, shardPodSpec.NodeName, shardPodSpec.Hostname, shardPodSpec.RestartPolicy)
	assertPodSpecSts(t, &statefulSetSc1, shardPodSpec.NodeName, shardPodSpec.Hostname, shardPodSpec.RestartPolicy)

	// assert Pod Spec for Mongos
	assertPodSpecSts(t, &statefulSetMongoS, mongosPodSpec.NodeName, mongosPodSpec.Hostname, mongosPodSpec.RestartPolicy)

	// assert Pod Spec for ConfigServer
	assertPodSpecSts(t, &statefulSetScConfig, configSrvPodSpec.NodeName, configSrvPodSpec.Hostname, configSrvPodSpec.RestartPolicy)

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
			VersionString: "5.0.0",
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
	assert.Equal(t, cf.Policies[0].PolicyType, controlledfeature.ExternallyManaged)
	assert.Equal(t, cf.Policies[1].PolicyType, controlledfeature.DisableMongodVersion)
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
		res, err := reconciler.Reconcile(context.TODO(), requestFromObject(sc))
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
		res, err := reconciler.Reconcile(context.TODO(), requestFromObject(sc))
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
			VersionString: "5.0.0",
		}
		conn := om.NewEmptyMockedOmConnection(context)
		return conn
	}

	checkReconcileSuccessful(t, reconciler, sc, client)

	mockedConn := om.CurrMockedConnection
	cf, _ := mockedConn.GetControlledFeature()

	assert.Len(t, cf.Policies, 3)

	assert.Equal(t, cf.ManagementSystem.Version, util.OperatorVersion)
	assert.Equal(t, cf.ManagementSystem.Name, util.OperatorName)

	var policies []controlledfeature.PolicyType
	for _, p := range cf.Policies {
		policies = append(policies, p.PolicyType)
	}

	assert.Contains(t, policies, controlledfeature.ExternallyManaged)
	assert.Contains(t, policies, controlledfeature.DisableAuthenticationMechanisms)
	assert.Contains(t, policies, controlledfeature.DisableMongodVersion)
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

// TestShardedCluster_ConfigMapAndSecretWatched verifies that config map and secret are added to the internal
// map that allows to watch them for changes
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

// TestShardedClusterTLSResourcesWatched verifies that TLS config map and secret are added to the internal
// map that allows to watch them for changes
func TestShardedClusterTLSResourcesWatched(t *testing.T) {
	sc := DefaultClusterBuilder().SetShardCountSpec(1).EnableTLS().SetTLSCA("custom-ca").Build()

	reconciler, client := defaultClusterReconciler(sc)

	addKubernetesTlsResources(client, sc)
	checkReconcileSuccessful(t, reconciler, sc, client)

	shard_secret := watch.Object{
		ResourceType: watch.Secret,
		Resource: types.NamespacedName{
			Namespace: sc.Namespace,
			Name:      sc.Name + "-0-cert",
		},
	}
	config_secret := watch.Object{
		ResourceType: watch.Secret,
		Resource: types.NamespacedName{
			Namespace: sc.Namespace,
			Name:      sc.Name + "-config-cert",
		},
	}
	mongos_secret := watch.Object{
		ResourceType: watch.Secret,
		Resource: types.NamespacedName{
			Namespace: sc.Namespace,
			Name:      sc.Name + "-mongos-cert",
		},
	}
	caKey := watch.Object{
		ResourceType: watch.ConfigMap,
		Resource: types.NamespacedName{
			Namespace: sc.Namespace,
			Name:      "custom-ca",
		},
	}
	assert.Contains(t, reconciler.WatchedResources, shard_secret)
	assert.Contains(t, reconciler.WatchedResources, config_secret)
	assert.Contains(t, reconciler.WatchedResources, mongos_secret)
	assert.Contains(t, reconciler.WatchedResources, caKey)

	sc.Spec.Security.TLSConfig.Enabled = false
	err := client.Update(context.TODO(), sc)
	assert.NoError(t, err)

	res, err := reconciler.Reconcile(context.TODO(), requestFromObject(sc))
	assert.Equal(t, reconcile.Result{}, res)
	assert.NoError(t, err)
	assert.NotContains(t, reconciler.WatchedResources, shard_secret)
	assert.NotContains(t, reconciler.WatchedResources, config_secret)
	assert.NotContains(t, reconciler.WatchedResources, mongos_secret)
	assert.NotContains(t, reconciler.WatchedResources, caKey)

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

	// configure backup for this project in Ops Manager in the mocked connection
	om.CurrMockedConnection = om.NewMockedOmConnection(om.NewDeployment())

	// 4 because configserver + num shards + 1 for entity to represent the sharded cluster iteself
	clusterIds := []string{"1", "2", "3", "4"}
	typeNames := []string{"SHARDED_REPLICA_SET", "REPLICA_SET", "REPLICA_SET", "CONFIG_SERVER_REPLICA_SET"}
	for i, clusterId := range clusterIds {
		om.CurrMockedConnection.UpdateBackupConfig(&backup.Config{
			ClusterId: clusterId,
			Status:    backup.Inactive,
		})

		om.CurrMockedConnection.BackupHostClusters[clusterId] = &backup.HostCluster{
			ClusterName: sc.Name,
			ShardName:   "ShardedCluster",
			TypeName:    typeNames[i],
		}
	}

	assertAllOtherBackupConfigsRemainUntouched := func(t *testing.T) {
		for _, configId := range []string{"2", "3", "4"} {
			config, err := om.CurrMockedConnection.ReadBackupConfig(configId)
			assert.NoError(t, err)
			// backup status should remain INACTIVE for all non "SHARDED_REPLICA_SET" configs.
			assert.Equal(t, backup.Inactive, config.Status)
		}
	}

	t.Run("Backup can be started", func(t *testing.T) {
		checkReconcileSuccessful(t, reconciler, sc, client)

		config, err := om.CurrMockedConnection.ReadBackupConfig("1")
		assert.NoError(t, err)
		assert.Equal(t, backup.Started, config.Status)
		assert.Equal(t, "1", config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
		assertAllOtherBackupConfigsRemainUntouched(t)
	})

	t.Run("Backup snapshot schedule tests", backupSnapshotScheduleTests(sc, client, reconciler, "1"))

	t.Run("Backup can be stopped", func(t *testing.T) {
		sc.Spec.Backup.Mode = "disabled"
		err := client.Update(context.TODO(), sc)
		assert.NoError(t, err)

		checkReconcileSuccessful(t, reconciler, sc, client)

		config, err := om.CurrMockedConnection.ReadBackupConfig("1")
		assert.NoError(t, err)
		assert.Equal(t, backup.Stopped, config.Status)
		assert.Equal(t, "1", config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
		assertAllOtherBackupConfigsRemainUntouched(t)
	})

	t.Run("Backup can be terminated", func(t *testing.T) {
		sc.Spec.Backup.Mode = "terminated"
		err := client.Update(context.TODO(), sc)
		assert.NoError(t, err)

		checkReconcileSuccessful(t, reconciler, sc, client)

		config, err := om.CurrMockedConnection.ReadBackupConfig("1")
		assert.NoError(t, err)
		assert.Equal(t, backup.Terminating, config.Status)
		assert.Equal(t, "1", config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
		assertAllOtherBackupConfigsRemainUntouched(t)
	})

}

// createShardedClusterTLSSecretsFromCustomCerts creates and populates all the required
// secrets required to enabled TLS with custom certs for all sharded cluster components.
func createShardedClusterTLSSecretsFromCustomCerts(sc *mdbv1.MongoDB, prefix string, client kubernetesClient.Client) {
	mongosSecret := secret.Builder().
		SetName(fmt.Sprintf("%s-%s-cert", prefix, sc.MongosRsName())).
		SetNamespace(sc.Namespace).SetDataType(corev1.SecretTypeTLS).
		Build()

	mongosSecret.Data["tls.crt"], mongosSecret.Data["tls.key"] = createMockCertAndKeyBytes()

	err := client.CreateSecret(mongosSecret)
	if err != nil {
		panic(err)
	}

	configSrvSecret := secret.Builder().
		SetName(fmt.Sprintf("%s-%s-cert", prefix, sc.ConfigRsName())).
		SetNamespace(sc.Namespace).SetDataType(corev1.SecretTypeTLS).
		Build()

	configSrvSecret.Data["tls.crt"], configSrvSecret.Data["tls.key"] = createMockCertAndKeyBytes()

	err = client.CreateSecret(configSrvSecret)
	if err != nil {
		panic(err)
	}

	for i := 0; i < sc.Spec.ShardCount; i++ {
		shardSecret := secret.Builder().
			SetName(fmt.Sprintf("%s-%s-cert", prefix, sc.ShardRsName(i))).
			SetNamespace(sc.Namespace).SetDataType(corev1.SecretTypeTLS).
			Build()

		shardSecret.Data["tls.crt"], shardSecret.Data["tls.key"] = createMockCertAndKeyBytes()

		err = client.CreateSecret(shardSecret)
		if err != nil {
			panic(err)
		}
	}
}

func TestTlsConfigPrefix_ForShardedCluster(t *testing.T) {
	sc := DefaultClusterBuilder().
		SetTLSConfig(mdbv1.TLSConfig{
			Enabled: false,
		}).
		Build()

	reconciler, client := defaultClusterReconciler(sc)

	createShardedClusterTLSSecretsFromCustomCerts(sc, "my-prefix", client)

	checkReconcileSuccessful(t, reconciler, sc, client)
}

func TestShardSpecificPodSpec(t *testing.T) {
	shardPodSpec := corev1.PodSpec{
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
	}

	shard0PodSpec := corev1.PodSpec{
		NodeName: "shard0-node-name",
		Containers: []corev1.Container{{
			Name:  "shard0-container",
			Image: "shard0-custom-image",
			VolumeMounts: []corev1.VolumeMount{{
				Name: "shard0-volume-mount",
			}},
		}},
		RestartPolicy: corev1.RestartPolicyAlways,
	}

	sc := DefaultClusterBuilder().SetName("shard-specific-pod-spec").EnableTLS().SetTLSCA("custom-ca").
		SetShardPodSpec(corev1.PodTemplateSpec{
			Spec: shardPodSpec,
		}).SetShardSpecificPodSpecTemplate([]corev1.PodTemplateSpec{
		{
			Spec: shard0PodSpec,
		},
	}).Build()

	reconciler, client := defaultClusterReconciler(sc)
	addKubernetesTlsResources(client, sc)
	checkReconcileSuccessful(t, reconciler, sc, client)

	// read the statefulsets from the cluster
	statefulSetSc0, err := client.GetStatefulSet(kube.ObjectKey(mock.TestNamespace, "shard-specific-pod-spec-0"))
	assert.NoError(t, err)
	statefulSetSc1, err := client.GetStatefulSet(kube.ObjectKey(mock.TestNamespace, "shard-specific-pod-spec-1"))
	assert.NoError(t, err)

	// shard0 should have the override
	assertPodSpecSts(t, &statefulSetSc0, shard0PodSpec.NodeName, shard0PodSpec.Hostname, shard0PodSpec.RestartPolicy)

	// shard1 should have the common one
	assertPodSpecSts(t, &statefulSetSc1, shardPodSpec.NodeName, shardPodSpec.Hostname, shardPodSpec.RestartPolicy)
}

func assertPodSpecSts(t *testing.T, sts *appsv1.StatefulSet, nodeName, hostName string, restartPolicy corev1.RestartPolicy) {

	podSpecTemplate := sts.Spec.Template.Spec
	// ensure values were passed to the stateful set
	assert.Equal(t, nodeName, podSpecTemplate.NodeName)
	assert.Equal(t, hostName, podSpecTemplate.Hostname)
	assert.Equal(t, restartPolicy, podSpecTemplate.RestartPolicy)

	assert.Equal(t, util.DatabaseContainerName, podSpecTemplate.Containers[0].Name, "Database container should always be first")
	assert.True(t, statefulset.VolumeMountWithNameExists(podSpecTemplate.Containers[0].VolumeMounts, construct.PvcNameDatabaseScripts))
}

func createDeploymentFromShardedCluster(updatable v1.CustomResourceReadWriter) om.Deployment {
	sh := updatable.(*mdbv1.MongoDB)

	mongosSts := construct.DatabaseStatefulSet(*sh, construct.MongosOptions(Replicas(sh.Spec.MongosCount), construct.GetPodEnvOptions()), nil)
	mongosProcesses := createMongosProcesses(mongosSts, sh, util.PEMKeyFilePathInContainer)
	configSvrSts := construct.DatabaseStatefulSet(*sh, construct.ConfigServerOptions(Replicas(sh.Spec.ConfigServerCount), construct.GetPodEnvOptions()), nil)

	configRs := buildReplicaSetFromProcesses(configSvrSts.Name, createConfigSrvProcesses(configSvrSts, sh, ""), sh)
	shards := make([]om.ReplicaSetWithProcesses, sh.Spec.ShardCount)
	for i := 0; i < sh.Spec.ShardCount; i++ {
		shardSts := construct.DatabaseStatefulSet(*sh, construct.ShardOptions(i, Replicas(sh.Spec.MongodsPerShardCount), construct.GetPodEnvOptions()), nil)
		shards[i] = buildReplicaSetFromProcesses(shardSts.Name, createShardProcesses(shardSts, sh, ""), sh)
	}

	d := om.NewDeployment()
	d.MergeShardedCluster(om.DeploymentShardedClusterMergeOptions{
		Name:            sh.Name,
		MongosProcesses: mongosProcesses,
		ConfigServerRs:  configRs,
		Shards:          shards,
		Finalizing:      false,
	})
	d.AddMonitoringAndBackup(zap.S(), sh.Spec.GetSecurity().IsTLSEnabled(), util.CAFilePathInContainer)
	return d
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
		DbCommonSpec: mdbv1.DbCommonSpec{
			Persistent: util.BooleanRef(false),
			ConnectionSpec: mdbv1.ConnectionSpec{
				OpsManagerConfig: &mdbv1.PrivateCloudConfig{
					ConfigMapRef: mdbv1.ConfigMapRef{
						Name: mock.TestProjectConfigMapName,
					},
				},
				Credentials: mock.TestCredentialsSecretName,
			},
			Version:      "3.6.4",
			ResourceType: mdbv1.ShardedCluster,

			Security: &mdbv1.Security{
				TLSConfig: &mdbv1.TLSConfig{},
				Authentication: &mdbv1.Authentication{
					Modes: []string{},
				},
			},
		},
		MongodbShardedClusterSizeConfig: sizeConfig,
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

func (b *ClusterBuilder) SetTLSCA(ca string) *ClusterBuilder {
	if b.Spec.Security == nil || b.Spec.Security.TLSConfig == nil {
		b.SetSecurity(mdbv1.Security{TLSConfig: &mdbv1.TLSConfig{}})
	}
	b.Spec.Security.TLSConfig.CA = ca
	return b
}

func (b *ClusterBuilder) SetTLSConfig(tlsConfig mdbv1.TLSConfig) *ClusterBuilder {
	if b.Spec.Security == nil {
		b.Spec.Security = &mdbv1.Security{}
	}
	b.Spec.Security.TLSConfig = &tlsConfig
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
	b.Spec.ShardPodSpec.PodTemplateWrapper.PodTemplate = &spec
	return b
}

func (b *ClusterBuilder) SetPodConfigSvrSpecTemplate(spec corev1.PodTemplateSpec) *ClusterBuilder {
	if b.Spec.ConfigSrvPodSpec == nil {
		b.Spec.ConfigSrvPodSpec = &mdbv1.MongoDbPodSpec{}
	}
	b.Spec.ConfigSrvPodSpec.PodTemplateWrapper.PodTemplate = &spec
	return b
}

func (b *ClusterBuilder) SetMongosPodSpecTemplate(spec corev1.PodTemplateSpec) *ClusterBuilder {
	if b.Spec.MongosPodSpec == nil {
		b.Spec.MongosPodSpec = &mdbv1.MongoDbPodSpec{}
	}
	b.Spec.MongosPodSpec.PodTemplateWrapper.PodTemplate = &spec
	return b
}

func (b *ClusterBuilder) SetShardSpecificPodSpecTemplate(specs []corev1.PodTemplateSpec) *ClusterBuilder {
	if b.Spec.ShardSpecificPodSpec == nil {
		b.Spec.ShardSpecificPodSpec = make([]mdbv1.MongoDbPodSpec, 0)
	}

	mongoDBPodSpec := make([]mdbv1.MongoDbPodSpec, len(specs))

	for n, e := range specs {
		mongoDBPodSpec[n] = mdbv1.MongoDbPodSpec{PodTemplateWrapper: mdbv1.PodTemplateSpecWrapper{
			PodTemplate: &e,
		}}
	}

	b.Spec.ShardSpecificPodSpec = mongoDBPodSpec
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
		SetDataField(util.OmBaseUrl, om.TestURL).
		SetDataField(util.OmOrgId, om.TestOrgID).
		SetDataField(util.OmProjectName, om.TestGroupName).
		Build()
}
