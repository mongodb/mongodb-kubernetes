package operator

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status/pvc"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/backup"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connection"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/controlledfeature"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/project"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1/common"
	mcoConstruct "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/controllers/construct"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/secret"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/test"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

func TestChangingFCVShardedCluster(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().Build()
	reconciler, _, cl, _, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	require.NoError(t, err)

	// Helper function to update and verify FCV
	verifyFCV := func(version, expectedFCV string, fcvOverride *string, t *testing.T) {
		if fcvOverride != nil {
			sc.Spec.FeatureCompatibilityVersion = fcvOverride
		}

		sc.Spec.Version = version
		_ = cl.Update(ctx, sc)
		checkReconcileSuccessful(ctx, t, reconciler, sc, cl)
		assert.Equal(t, expectedFCV, sc.Status.FeatureCompatibilityVersion)
	}

	testFCVsCases(t, verifyFCV)
}

func TestReconcileCreateShardedCluster(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().Build()

	reconciler, _, kubeClient, omConnectionFactory, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	c := kubeClient
	require.NoError(t, err)

	checkReconcileSuccessful(ctx, t, reconciler, sc, c)
	assert.Len(t, mock.GetMapForObject(c, &corev1.Secret{}), 2)
	assert.Len(t, mock.GetMapForObject(c, &corev1.Service{}), 3)
	assert.Len(t, mock.GetMapForObject(c, &appsv1.StatefulSet{}), 4)
	assert.Equal(t, getStsReplicas(ctx, c, kube.ObjectKey(sc.Namespace, sc.ConfigRsName()), t), int32(sc.Spec.ConfigServerCount))
	assert.Equal(t, getStsReplicas(ctx, c, kube.ObjectKey(sc.Namespace, sc.MongosRsName()), t), int32(sc.Spec.MongosCount))
	assert.Equal(t, getStsReplicas(ctx, c, kube.ObjectKey(sc.Namespace, sc.ShardRsName(0)), t), int32(sc.Spec.MongodsPerShardCount))
	assert.Equal(t, getStsReplicas(ctx, c, kube.ObjectKey(sc.Namespace, sc.ShardRsName(1)), t), int32(sc.Spec.MongodsPerShardCount))

	mockedConn := omConnectionFactory.GetConnection().(*om.MockedOmConnection)
	expectedDeployment := createDeploymentFromShardedCluster(t, sc)
	if !mockedConn.CheckDeployment(t, expectedDeployment, "auth", "tls") {
		// this is to diagnose problems using visual diff as the automation config is large
		// it is very difficult to spot what's wrong using assert's Equal dump
		// NOTE: this sometimes get mangled in IntelliJ's console. If it's not showing correctly, put a time.Sleep here.
		fmt.Printf("deployment diff:\n%s", visualJsonDiffOfAnyObjects(t, expectedDeployment, mockedConn.GetDeployment()))
	}
	mockedConn.CheckNumberOfUpdateRequests(t, 2)
	// we don't remove hosts from monitoring if there is no scale down
	mockedConn.CheckOperationsDidntHappen(t, reflect.ValueOf(mockedConn.GetHosts), reflect.ValueOf(mockedConn.RemoveHost))
}

// TestReconcileCreateSingleClusterShardedClusterWithNoServiceMeshSimplest assumes only Services for Mongos
// will be created.
func TestReconcileCreateSingleClusterShardedClusterWithExternalDomainSimplest(t *testing.T) {
	// given
	ctx := context.Background()
	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory()

	sc := test.DefaultClusterBuilder().
		SetExternalAccessDomain(test.ExampleExternalClusterDomains).
		SetExternalAccessDomainAnnotations(test.SingleClusterAnnotationsWithPlaceholders).
		Build()
	fakeClient := mock.NewEmptyFakeClientBuilder().
		WithObjects(sc).
		WithObjects(mock.GetDefaultResources()...).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: mock.GetFakeClientInterceptorGetFunc(omConnectionFactory, true, true),
		}).
		Build()

	kubeClient := kubernetesClient.NewClient(fakeClient)
	reconciler, _, _ := newShardedClusterReconcilerFromResource(ctx, nil, "", "", sc, nil, kubeClient, omConnectionFactory)

	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		var allHostnames []string
		// Note that only Mongos uses external domains. The other components use domains internal to the cluster.
		mongosHostNames, _ := dns.GetDNSNames(sc.MongosRsName(), sc.ServiceName(), sc.Namespace, "", sc.Spec.MongosCount, &test.ExampleExternalClusterDomains.SingleClusterDomain)
		allHostnames = append(allHostnames, mongosHostNames...)
		configServersHostNames, _ := dns.GetDNSNames(sc.ConfigRsName(), sc.ConfigSrvServiceName(), sc.Namespace, "", sc.Spec.ConfigServerCount, &test.NoneExternalClusterDomains.ConfigServerExternalDomain)
		allHostnames = append(allHostnames, configServersHostNames...)
		for shardIdx := range sc.Spec.ShardCount {
			shardHostNames, _ := dns.GetDNSNames(sc.ShardRsName(shardIdx), sc.ShardServiceName(), sc.Namespace, "", sc.Spec.MongodsPerShardCount, &test.NoneExternalClusterDomains.ShardsExternalDomain)
			allHostnames = append(allHostnames, shardHostNames...)
		}

		connection.(*om.MockedOmConnection).AddHosts(allHostnames)
	})

	// when
	checkReconcileSuccessful(ctx, t, reconciler, sc, kubeClient)

	// then
	memberClusterChecks := newClusterChecks(t, multicluster.LegacyCentralClusterName, 0, sc.Namespace, kubeClient)

	mongosStatefulSetName := fmt.Sprintf("%s-mongos", sc.Name)
	memberClusterChecks.checkExternalServices(ctx, mongosStatefulSetName, sc.Spec.MongosCount)
	memberClusterChecks.checkPerPodServicesDontExist(ctx, mongosStatefulSetName, sc.Spec.MongosCount)
	memberClusterChecks.checkServiceAnnotations(ctx, mongosStatefulSetName, sc.Spec.MongosCount, sc, multicluster.LegacyCentralClusterName, 0, test.ExampleExternalClusterDomains.SingleClusterDomain)

	configServerStatefulSetName := fmt.Sprintf("%s-config", sc.Name)
	memberClusterChecks.checkExternalServicesDontExist(ctx, configServerStatefulSetName, sc.Spec.ConfigServerCount)
	memberClusterChecks.checkPerPodServicesDontExist(ctx, configServerStatefulSetName, sc.Spec.ConfigServerCount)
	// This is something to be unified - why MC and SC Services are called differently?
	configServerInternalServiceName := fmt.Sprintf("%s-cs", sc.Name)
	memberClusterChecks.checkServiceExists(ctx, configServerInternalServiceName)

	memberClusterChecks.checkExternalServicesDontExist(ctx, fmt.Sprintf("%s-config", sc.Name), sc.Spec.ConfigServerCount)
	for shardIdx := 0; shardIdx < sc.Spec.ShardCount; shardIdx++ {
		shardStatefulSetName := fmt.Sprintf("%s-%d", sc.Name, shardIdx)
		memberClusterChecks.checkExternalServicesDontExist(ctx, shardStatefulSetName, sc.Spec.ShardCount)
		memberClusterChecks.checkPerPodServicesDontExist(ctx, shardStatefulSetName, sc.Spec.ShardCount)
		// This is something to be unified - why MC and SC Services are called differently?
		shardInternalServiceName := fmt.Sprintf("%s-sh", sc.Name)
		memberClusterChecks.checkServiceExists(ctx, shardInternalServiceName)
	}
}

func getStsReplicas(ctx context.Context, client kubernetesClient.Client, key client.ObjectKey, t *testing.T) int32 {
	sts, err := client.GetStatefulSet(ctx, key)
	require.NoError(t, err)

	return *sts.Spec.Replicas
}

func TestShardedClusterRace(t *testing.T) {
	ctx := context.Background()
	sc1, cfgMap1, projectName1 := buildShardedClusterWithCustomProject("my-sh1")
	sc2, cfgMap2, projectName2 := buildShardedClusterWithCustomProject("my-sh2")
	sc3, cfgMap3, projectName3 := buildShardedClusterWithCustomProject("my-sh3")

	resourceToProjectMapping := map[string]string{
		"my-sh1": projectName1,
		"my-sh2": projectName2,
		"my-sh3": projectName3,
	}

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory().WithResourceToProjectMapping(resourceToProjectMapping)
	fakeClient := mock.NewEmptyFakeClientBuilder().
		WithObjects(sc1, sc2, sc3).
		WithObjects(cfgMap1, cfgMap2, cfgMap3).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: mock.GetFakeClientInterceptorGetFunc(omConnectionFactory, true, true),
		}).
		WithObjects(mock.GetDefaultResources()...).
		Build()

	reconciler := newShardedClusterReconciler(ctx, fakeClient, nil, "fake-initDatabaseNonStaticImageVersion", "fake-databaseNonStaticImageVersion", false, false, nil, omConnectionFactory.GetConnectionFunc)

	testConcurrentReconciles(ctx, t, fakeClient, reconciler, sc1, sc2, sc3)
}

func buildShardedClusterWithCustomProject(scName string) (*mdbv1.MongoDB, *corev1.ConfigMap, string) {
	configMapName := mock.TestProjectConfigMapName + "-" + scName
	projectName := om.TestGroupName + "-" + scName

	return test.DefaultClusterBuilder().
		SetName(scName).
		SetOpsManagerConfigMapName(configMapName).
		SetShardCountSpec(4).
		SetShardCountStatus(4).
		Build(), mock.GetProjectConfigMap(configMapName, projectName, ""), projectName
}

// TODO this is to be removed as it's testing whether we scale down entire shards one by one, but it's actually testing only scale by one; and we actually don't scale one by one but prune all the shards to be removed immediately"
func TestReconcileCreateShardedCluster_ScaleDown(t *testing.T) {
	t.Skip("this test should probably be deleted")
	ctx := context.Background()
	// First creation
	sc := test.DefaultClusterBuilder().SetShardCountSpec(4).SetShardCountStatus(4).Build()
	reconciler, _, clusterClient, omConnectionFactory, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	require.NoError(t, err)

	checkReconcileSuccessful(ctx, t, reconciler, sc, clusterClient)

	mockedConn := omConnectionFactory.GetConnection().(*om.MockedOmConnection)
	mockedConn.CleanHistory()

	// Scale down then
	sc.Spec.ShardCount = 3

	_ = clusterClient.Update(ctx, sc)

	checkReconcileSuccessful(ctx, t, reconciler, sc, clusterClient)

	// Two deployment modifications are expected
	mockedConn.CheckOrderOfOperations(t, reflect.ValueOf(mockedConn.ReadUpdateDeployment), reflect.ValueOf(mockedConn.ReadUpdateDeployment))

	// todo ideally we need to check the "transitive" deployment that was created on first step, but let's check the
	// final version at least

	// the updated deployment should reflect that of a ShardedCluster with one fewer member
	scWith3Members := test.DefaultClusterBuilder().SetShardCountStatus(3).SetShardCountSpec(3).Build()
	mockedConn.CheckDeployment(t, createDeploymentFromShardedCluster(t, scWith3Members), "auth", "tls")

	// No matter how many members we scale down by, we will only have one fewer each reconciliation
	assert.Len(t, mock.GetMapForObject(clusterClient, &appsv1.StatefulSet{}), 5)
}

func TestShardedClusterReconcileContainerImages(t *testing.T) {
	databaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_1_0_0", util.NonStaticDatabaseEnterpriseImage)
	initDatabaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_2_0_0", util.InitDatabaseImageUrlEnv)

	imageUrlsMock := images.ImageUrls{
		databaseRelatedImageEnv:     "quay.io/mongodb/mongodb-kubernetes-database:@sha256:MONGODB_DATABASE",
		initDatabaseRelatedImageEnv: "quay.io/mongodb/mongodb-kubernetes-init-database:@sha256:MONGODB_INIT_DATABASE",
	}

	ctx := context.Background()
	sc := test.DefaultClusterBuilder().SetVersion("8.0.0").SetShardCountSpec(1).Build()

	reconciler, _, kubeClient, _, err := defaultClusterReconciler(ctx, imageUrlsMock, "2.0.0", "1.0.0", sc, nil)
	require.NoError(t, err)

	checkReconcileSuccessful(ctx, t, reconciler, sc, kubeClient)

	for stsAlias, stsName := range map[string]string{
		"config":  sc.ConfigRsName(),
		"mongos":  sc.MongosRsName(),
		"shard-0": sc.ShardRsName(0),
	} {
		t.Run(stsAlias, func(t *testing.T) {
			sts := &appsv1.StatefulSet{}
			err = kubeClient.Get(ctx, kube.ObjectKey(sc.Namespace, stsName), sts)
			assert.NoError(t, err)

			require.Len(t, sts.Spec.Template.Spec.InitContainers, 1)
			require.Len(t, sts.Spec.Template.Spec.Containers, 1)

			assert.Equal(t, "quay.io/mongodb/mongodb-kubernetes-init-database:@sha256:MONGODB_INIT_DATABASE", sts.Spec.Template.Spec.InitContainers[0].Image)
			assert.Equal(t, "quay.io/mongodb/mongodb-kubernetes-database:@sha256:MONGODB_DATABASE", sts.Spec.Template.Spec.Containers[0].Image)
		})
	}
}

func TestShardedClusterReconcileContainerImagesWithStaticArchitecture(t *testing.T) {
	t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))

	databaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_8_0_0_ubi9", mcoConstruct.MongodbImageEnv)

	ctx := context.Background()
	sc := test.DefaultClusterBuilder().SetVersion("8.0.0").SetShardCountSpec(1).Build()

	imageUrlsMock := images.ImageUrls{
		architectures.MdbAgentImageRepo: "quay.io/mongodb/mongodb-agent-ubi",
		mcoConstruct.MongodbImageEnv:    "quay.io/mongodb/mongodb-enterprise-server",
		databaseRelatedImageEnv:         "quay.io/mongodb/mongodb-enterprise-server:@sha256:MONGODB_DATABASE",
	}

	reconciler, _, kubeClient, omConnectionFactory, err := defaultClusterReconciler(ctx, imageUrlsMock, "", "", sc, nil)
	require.NoError(t, err)

	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		connection.(*om.MockedOmConnection).SetAgentVersion("12.0.30.7791-1", "")
	})

	checkReconcileSuccessful(ctx, t, reconciler, sc, kubeClient)

	for stsAlias, stsName := range map[string]string{
		"config":  sc.ConfigRsName(),
		"mongos":  sc.MongosRsName(),
		"shard-0": sc.ShardRsName(0),
	} {
		t.Run(stsAlias, func(t *testing.T) {
			sts := &appsv1.StatefulSet{}
			err = kubeClient.Get(ctx, kube.ObjectKey(sc.Namespace, stsName), sts)
			assert.NoError(t, err)

			assert.Len(t, sts.Spec.Template.Spec.InitContainers, 0)
			require.Len(t, sts.Spec.Template.Spec.Containers, 3)

			// Version from OM
			VerifyStaticContainers(t, sts.Spec.Template.Spec.Containers)
		})
	}
}

func TestReconcilePVCResizeShardedCluster(t *testing.T) {
	ctx := context.Background()
	// First creation
	sc := test.DefaultClusterBuilder().SetShardCountSpec(2).SetShardCountStatus(2).Build()
	persistence := common.Persistence{
		SingleConfig: &common.PersistenceConfig{
			Storage: "1Gi",
		},
	}
	sc.Spec.Persistent = util.BooleanRef(true)
	sc.Spec.ConfigSrvPodSpec.Persistence = &persistence
	sc.Spec.ShardPodSpec.Persistence = &persistence
	reconciler, _, c, _, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	assert.NoError(t, err)

	// first, we create the shardedCluster with sts and pvc,
	// no resize happening, even after running reconcile multiple times
	checkReconcileSuccessful(ctx, t, reconciler, sc, c)
	testNoResize(t, c, ctx, sc)

	checkReconcileSuccessful(ctx, t, reconciler, sc, c)
	testNoResize(t, c, ctx, sc)

	createdConfigPVCs, createdSharded0PVCs, createdSharded1PVCs := getPVCs(t, c, ctx, sc)

	newSize := "2Gi"
	// increasing the storage now and start a new reconciliation
	persistence.SingleConfig.Storage = newSize

	sc.Spec.ConfigSrvPodSpec.Persistence = &persistence
	sc.Spec.ShardPodSpec.Persistence = &persistence
	err = c.Update(ctx, sc)
	assert.NoError(t, err)

	_, e := reconciler.Reconcile(ctx, requestFromObject(sc))
	assert.NoError(t, e)

	// its only one sts in the pvc status, since we haven't started the next one yet
	testMDBStatus(t, c, ctx, sc, status.PhasePending, status.PVCS{{Phase: pvc.PhasePVCResize, StatefulsetName: test.SCBuilderDefaultName + "-config"}})

	testPVCSizeHasIncreased(t, c, ctx, newSize, test.SCBuilderDefaultName+"-config")

	// Running the same resize makes no difference, we are still resizing
	_, e = reconciler.Reconcile(ctx, requestFromObject(sc))
	assert.NoError(t, e)

	testMDBStatus(t, c, ctx, sc, status.PhasePending, status.PVCS{{Phase: pvc.PhasePVCResize, StatefulsetName: test.SCBuilderDefaultName + "-config"}})

	for _, claim := range createdConfigPVCs {
		setPVCWithUpdatedResource(ctx, t, c, &claim)
	}

	// Running reconcile again should go into orphan
	_, e = reconciler.Reconcile(ctx, requestFromObject(sc))
	assert.NoError(t, e)

	// the second pvc is now getting resized
	testMDBStatus(t, c, ctx, sc, status.PhasePending, status.PVCS{
		{Phase: pvc.PhaseSTSOrphaned, StatefulsetName: sc.Name + "-config"},
		{Phase: pvc.PhasePVCResize, StatefulsetName: sc.Name + "-0"},
	})
	testPVCSizeHasIncreased(t, c, ctx, newSize, sc.Name+"-0")
	testStatefulsetHasAnnotationAndCorrectSize(t, c, ctx, sc.Namespace, sc.Name+"-config")

	for _, claim := range createdSharded0PVCs {
		setPVCWithUpdatedResource(ctx, t, c, &claim)
	}

	// Running reconcile again second pvcState should go into orphan, third one should start
	_, e = reconciler.Reconcile(ctx, requestFromObject(sc))
	assert.NoError(t, e)

	testMDBStatus(t, c, ctx, sc, status.PhasePending, status.PVCS{
		{Phase: pvc.PhaseSTSOrphaned, StatefulsetName: sc.Name + "-config"},
		{Phase: pvc.PhaseSTSOrphaned, StatefulsetName: sc.Name + "-0"},
		{Phase: pvc.PhasePVCResize, StatefulsetName: sc.Name + "-1"},
	})
	testPVCSizeHasIncreased(t, c, ctx, newSize, sc.Name+"-1")
	testStatefulsetHasAnnotationAndCorrectSize(t, c, ctx, sc.Namespace, sc.Name+"-0")

	for _, claim := range createdSharded1PVCs {
		setPVCWithUpdatedResource(ctx, t, c, &claim)
	}

	// We move from resize → orphaned and in the final call in the reconciling to running and
	// remove the PVCs.
	_, err = reconciler.Reconcile(ctx, requestFromObject(sc))
	assert.NoError(t, err)

	// We are now in the running phase, since all statefulsets have finished resizing; therefore,
	// no pvc phase is shown anymore
	testMDBStatus(t, c, ctx, sc, status.PhaseRunning, nil)
	testStatefulsetHasAnnotationAndCorrectSize(t, c, ctx, sc.Namespace, sc.Name+"-1")
}

func testStatefulsetHasAnnotationAndCorrectSize(t *testing.T, c client.Client, ctx context.Context, namespace, stsName string) {
	// verify config-sts has been re-created with new annotation
	sts := &appsv1.StatefulSet{}
	err := c.Get(ctx, kube.ObjectKey(namespace, stsName), sts)
	assert.NoError(t, err)
	assert.Equal(t, "[{\"Name\":\"data\",\"Size\":\"2Gi\"}]", sts.Spec.Template.Annotations["mongodb.com/storageSize"])
	assert.Equal(t, "2Gi", sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage().String())
	assert.Len(t, sts.Spec.VolumeClaimTemplates, 1)
}

func testMDBStatus(t *testing.T, c kubernetesClient.Client, ctx context.Context, sc *mdbv1.MongoDB, expectedMDBPhase status.Phase, expectedPVCS status.PVCS) {
	mdb := mdbv1.MongoDB{}
	err := c.Get(ctx, kube.ObjectKey(sc.Namespace, sc.Name), &mdb)
	assert.NoError(t, err)
	require.Equal(t, expectedMDBPhase, mdb.Status.Phase)
	require.Equal(t, expectedPVCS, mdb.Status.PVCs)
}

func getPVCs(t *testing.T, c kubernetesClient.Client, ctx context.Context, sc *mdbv1.MongoDB) ([]corev1.PersistentVolumeClaim, []corev1.PersistentVolumeClaim, []corev1.PersistentVolumeClaim) {
	sts, err := c.GetStatefulSet(ctx, kube.ObjectKey(sc.Namespace, sc.Name+"-config"))
	assert.NoError(t, err)
	createdConfigPVCs := createPVCs(t, sts, c)

	sts, err = c.GetStatefulSet(ctx, kube.ObjectKey(sc.Namespace, sc.Name+"-0"))
	assert.NoError(t, err)
	createdSharded0PVCs := createPVCs(t, sts, c)

	sts, err = c.GetStatefulSet(ctx, kube.ObjectKey(sc.Namespace, sc.Name+"-1"))
	assert.NoError(t, err)
	createdSharded1PVCs := createPVCs(t, sts, c)
	return createdConfigPVCs, createdSharded0PVCs, createdSharded1PVCs
}

func testNoResize(t *testing.T, c kubernetesClient.Client, ctx context.Context, sc *mdbv1.MongoDB) {
	mdb := mdbv1.MongoDB{}
	err := c.Get(ctx, kube.ObjectKey(sc.Namespace, sc.Name), &mdb)
	assert.NoError(t, err)
	assert.Nil(t, mdb.Status.PVCs)
}

func testPVCSizeHasIncreased(t *testing.T, c client.Client, ctx context.Context, newSize string, pvcName string) {
	list := corev1.PersistentVolumeClaimList{}
	err := c.List(ctx, &list)
	require.NoError(t, err)
	for _, item := range list.Items {
		if strings.Contains(item.Name, pvcName) {
			assert.Equal(t, item.Spec.Resources.Requests.Storage().String(), newSize)
		}
	}
	require.NoError(t, err)
}

func createPVCs(t *testing.T, sts appsv1.StatefulSet, c client.Writer) []corev1.PersistentVolumeClaim {
	var createdPVCs []corev1.PersistentVolumeClaim
	// Manually create the PVCs that would be generated by the StatefulSet controller
	for i := 0; i < int(*sts.Spec.Replicas); i++ {
		pvcName := fmt.Sprintf("%s-%s-%d", sts.Spec.VolumeClaimTemplates[0].Name, sts.Name, i)
		p := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pvcName,
				Namespace: sts.Namespace,
				Labels:    sts.Spec.Template.Labels,
			},
			Spec: sts.Spec.VolumeClaimTemplates[0].Spec,
		}
		err := c.Create(context.TODO(), p)
		require.NoError(t, err)
		createdPVCs = append(createdPVCs, *p)
	}
	return createdPVCs
}

// TestAddDeleteShardedCluster checks that no state is left in OpsManager on removal of the sharded cluster
func TestAddDeleteShardedCluster(t *testing.T) {
	ctx := context.Background()
	// First we need to create a sharded cluster
	sc := test.DefaultClusterBuilder().Build()

	reconciler, _, clusterClient, omConnectionFactory, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		connection.(*om.MockedOmConnection).AgentsDelayCount = 1
	})
	require.NoError(t, err)

	checkReconcileSuccessful(ctx, t, reconciler, sc, clusterClient)
	// Now delete it
	assert.NoError(t, reconciler.OnDelete(ctx, sc, zap.S()))

	// Operator doesn't mutate K8s state, so we don't check its changes, only OM
	mockedOmConnection := omConnectionFactory.GetConnection().(*om.MockedOmConnection)
	mockedOmConnection.CheckResourcesDeleted(t)

	mockedOmConnection.CheckOrderOfOperations(t,
		reflect.ValueOf(mockedOmConnection.ReadUpdateDeployment), reflect.ValueOf(mockedOmConnection.ReadAutomationStatus),
		reflect.ValueOf(mockedOmConnection.GetHosts), reflect.ValueOf(mockedOmConnection.RemoveHost))
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
	ctx := context.Background()

	initialState := MultiClusterShardedScalingStep{
		shardCount: 2,
		configServerDistribution: map[string]int{
			multicluster.LegacyCentralClusterName: 3,
		},
		shardDistribution: map[string]int{
			multicluster.LegacyCentralClusterName: 4,
		},
	}

	scBeforeScale := test.DefaultClusterBuilder().
		SetConfigServerCountSpec(3).
		SetMongodsPerShardCountSpec(4).
		Build()

	scAfterScale := test.DefaultClusterBuilder().
		SetConfigServerCountSpec(2).
		SetMongodsPerShardCountSpec(3).
		Build()

	omConnectionFactory := om.NewCachedOMConnectionFactoryWithInitializedConnection(om.NewMockedOmConnection(createDeploymentFromShardedCluster(t, scBeforeScale)))
	kubeClient, _ := mock.NewDefaultFakeClient(scAfterScale)
	// Store the initial scaling status in state configmap
	assert.NoError(t, createMockStateConfigMap(kubeClient, mock.TestNamespace, scBeforeScale.Name, initialState))
	_, reconcileHelper, err := newShardedClusterReconcilerFromResource(ctx, nil, "", "", scAfterScale, nil, kubeClient, omConnectionFactory)
	assert.NoError(t, err)
	assert.NoError(t, reconcileHelper.prepareScaleDownShardedCluster(omConnectionFactory.GetConnection(), zap.S()))

	// create the expected deployment from the sharded cluster that has not yet scaled
	// expected change of state: rs members are marked unvoted
	expectedDeployment := createDeploymentFromShardedCluster(t, scBeforeScale)
	firstConfig := scAfterScale.ConfigRsName() + "-2"
	firstShard := scAfterScale.ShardRsName(0) + "-3"
	secondShard := scAfterScale.ShardRsName(1) + "-3"

	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(scAfterScale.ConfigRsName(), []string{firstConfig}))
	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(scAfterScale.ShardRsName(0), []string{firstShard}))
	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(scAfterScale.ShardRsName(1), []string{secondShard}))

	mockedOmConnection := omConnectionFactory.GetConnection().(*om.MockedOmConnection)
	mockedOmConnection.CheckNumberOfUpdateRequests(t, 1)
	mockedOmConnection.CheckDeployment(t, expectedDeployment)
	// we don't remove hosts from monitoring at this stage
	mockedOmConnection.CheckMonitoredHostsRemoved(t, []string{})
}

// TestPrepareScaleDownShardedCluster_ShardsUpMongodsDown checks the situation when shards count increases and mongods
// count per shard is decreased - scale down operation is expected to be called only for existing shards
func TestPrepareScaleDownShardedCluster_ShardsUpMongodsDown(t *testing.T) {
	ctx := context.Background()

	initialState := MultiClusterShardedScalingStep{
		shardCount: 4,
		shardDistribution: map[string]int{
			multicluster.LegacyCentralClusterName: 4,
		},
	}

	scBeforeScale := test.DefaultClusterBuilder().
		SetShardCountSpec(4).
		SetMongodsPerShardCountSpec(4).
		Build()

	scAfterScale := test.DefaultClusterBuilder().
		SetShardCountSpec(2).
		SetMongodsPerShardCountSpec(3).
		Build()

	omConnectionFactory := om.NewCachedOMConnectionFactoryWithInitializedConnection(om.NewMockedOmConnection(createDeploymentFromShardedCluster(t, scBeforeScale)))
	kubeClient, _ := mock.NewDefaultFakeClient(scAfterScale)
	assert.NoError(t, createMockStateConfigMap(kubeClient, mock.TestNamespace, scBeforeScale.Name, initialState))
	_, reconcileHelper, err := newShardedClusterReconcilerFromResource(ctx, nil, "", "", scAfterScale, nil, kubeClient, omConnectionFactory)
	assert.NoError(t, err)

	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		deployment := createDeploymentFromShardedCluster(t, scBeforeScale)
		if _, err := connection.UpdateDeployment(deployment); err != nil {
			panic(err)
		}
		connection.(*om.MockedOmConnection).AddHosts(deployment.GetAllHostnames())
		connection.(*om.MockedOmConnection).CleanHistory()
	})

	assert.NoError(t, reconcileHelper.prepareScaleDownShardedCluster(omConnectionFactory.GetConnection(), zap.S()))

	// expected change of state: rs members are marked unvoted only for two shards (old state)
	expectedDeployment := createDeploymentFromShardedCluster(t, scBeforeScale)
	firstShard := scBeforeScale.ShardRsName(0) + "-3"
	secondShard := scBeforeScale.ShardRsName(1) + "-3"
	thirdShard := scBeforeScale.ShardRsName(2) + "-3"
	fourthShard := scBeforeScale.ShardRsName(3) + "-3"

	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(scBeforeScale.ShardRsName(0), []string{firstShard}))
	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(scBeforeScale.ShardRsName(1), []string{secondShard}))
	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(scBeforeScale.ShardRsName(2), []string{thirdShard}))
	assert.NoError(t, expectedDeployment.MarkRsMembersUnvoted(scBeforeScale.ShardRsName(3), []string{fourthShard}))

	mockedOmConnection := omConnectionFactory.GetConnection().(*om.MockedOmConnection)
	mockedOmConnection.CheckNumberOfUpdateRequests(t, 1)
	mockedOmConnection.CheckDeployment(t, expectedDeployment)
	// we don't remove hosts from monitoring at this stage
	mockedOmConnection.CheckOperationsDidntHappen(t, reflect.ValueOf(mockedOmConnection.RemoveHost))
}

func TestConstructConfigSrv(t *testing.T) {
	sc := test.DefaultClusterBuilder().Build()
	configSrvSpec := createConfigSrvSpec(sc)
	assert.NotPanics(t, func() {
		construct.DatabaseStatefulSet(*sc, construct.ConfigServerOptions(configSrvSpec, multicluster.LegacyCentralClusterName, construct.GetPodEnvOptions()), zap.S())
	})
}

// TestPrepareScaleDownShardedCluster_OnlyMongos checks that if only mongos processes are scaled down - then no preliminary
// actions are done
func TestPrepareScaleDownShardedCluster_OnlyMongos(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().SetMongosCountStatus(4).SetMongosCountSpec(2).Build()
	_, reconcileHelper, _, omConnectionFactory, _ := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	oldDeployment := createDeploymentFromShardedCluster(t, sc)
	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		if _, err := connection.UpdateDeployment(oldDeployment); err != nil {
			panic(err)
		}
		connection.(*om.MockedOmConnection).CleanHistory()
	})

	// necessary otherwise next omConnectionFactory.GetConnection() will return nil as the connectionFactoryFunc hasn't been called yet
	initializeOMConnection(t, ctx, reconcileHelper, sc, zap.S(), omConnectionFactory)

	assert.NoError(t, reconcileHelper.prepareScaleDownShardedCluster(omConnectionFactory.GetConnection(), zap.S()))
	mockedOmConnection := omConnectionFactory.GetConnection().(*om.MockedOmConnection)
	mockedOmConnection.CheckNumberOfUpdateRequests(t, 0)
	mockedOmConnection.CheckDeployment(t, createDeploymentFromShardedCluster(t, sc))
	mockedOmConnection.CheckOperationsDidntHappen(t, reflect.ValueOf(mockedOmConnection.RemoveHost))
}

// initializeOMConnection reads project config maps and initializes connection to OM.
// It's useful for cases when the full Reconcile is not caller or the reconcile is not calling omConnectionFactoryFunc to get (create and cache) actual connection.
// Without it subsequent calls to omConnectionFactory.GetConnection() will return nil.
func initializeOMConnection(t *testing.T, ctx context.Context, reconcileHelper *ShardedClusterReconcileHelper, sc *mdbv1.MongoDB, log *zap.SugaredLogger, omConnectionFactory *om.CachedOMConnectionFactory) {
	projectConfig, credsConfig, err := project.ReadConfigAndCredentials(ctx, reconcileHelper.commonController.client, reconcileHelper.commonController.SecretClient, sc, log)
	require.NoError(t, err)
	_, _, err = connection.PrepareOpsManagerConnection(ctx, reconcileHelper.commonController.SecretClient, projectConfig, credsConfig, omConnectionFactory.GetConnectionFunc, sc.Namespace, log)
	require.NoError(t, err)
}

// TestUpdateOmDeploymentShardedCluster_HostsRemovedFromMonitoring verifies that if scale down operation was performed -
// hosts are removed
func TestUpdateOmDeploymentShardedCluster_HostsRemovedFromMonitoring(t *testing.T) {
	ctx := context.Background()

	initialState := MultiClusterShardedScalingStep{
		mongosDistribution: map[string]int{
			multicluster.LegacyCentralClusterName: 2,
		},
		configServerDistribution: map[string]int{
			multicluster.LegacyCentralClusterName: 4,
		},
	}

	sc := test.DefaultClusterBuilder().
		SetMongosCountSpec(2).
		SetConfigServerCountSpec(4).
		Build()

	// we need to create a different sharded cluster that is currently in the process of scaling down
	scScaledDown := test.DefaultClusterBuilder().
		SetMongosCountSpec(1).
		SetConfigServerCountSpec(3).
		Build()

	omConnectionFactory := om.NewCachedOMConnectionFactoryWithInitializedConnection(om.NewMockedOmConnection(createDeploymentFromShardedCluster(t, sc)))
	kubeClient, _ := mock.NewDefaultFakeClient(sc)
	assert.NoError(t, createMockStateConfigMap(kubeClient, mock.TestNamespace, sc.Name, initialState))
	_, reconcileHelper, err := newShardedClusterReconcilerFromResource(ctx, nil, "", "", scScaledDown, nil, kubeClient, omConnectionFactory)
	assert.NoError(t, err)

	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		// the initial deployment we create should have all processes
		deployment := createDeploymentFromShardedCluster(t, sc)
		if _, err := connection.UpdateDeployment(deployment); err != nil {
			panic(err)
		}
		connection.(*om.MockedOmConnection).AddHosts(deployment.GetAllHostnames())
		connection.(*om.MockedOmConnection).CleanHistory()
	})

	// updateOmDeploymentShardedCluster checks an element from ac.Auth.DeploymentAuthMechanisms
	// so we need to ensure it has a non-nil value. An empty list implies no authentication
	_ = omConnectionFactory.GetConnection().ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
		ac.Auth.DeploymentAuthMechanisms = []string{}
		return nil
	}, nil)

	mockOm := omConnectionFactory.GetConnection().(*om.MockedOmConnection)
	assert.Equal(t, workflow.OK(), reconcileHelper.updateOmDeploymentShardedCluster(ctx, mockOm, scScaledDown, deploymentOptions{podEnvVars: &env.PodEnvVars{ProjectID: "abcd"}}, false, zap.S()))

	mockOm.CheckOrderOfOperations(t, reflect.ValueOf(mockOm.ReadUpdateDeployment), reflect.ValueOf(mockOm.RemoveHost))

	// expected change of state: no unvoting - just monitoring deleted
	firstConfig := sc.ConfigRsName() + "-3"
	firstMongos := sc.MongosRsName() + "-1"

	mockOm.CheckMonitoredHostsRemoved(t, []string{
		firstConfig + fmt.Sprintf(".%s-cs.mongodb.svc.cluster.local", scScaledDown.Name),
		firstMongos + fmt.Sprintf(".%s-svc.mongodb.svc.cluster.local", scScaledDown.Name),
	})
}

// CLOUDP-32765: checks that pod anti affinity rule spreads mongods inside one shard, not inside all shards
func TestPodAntiaffinity_MongodsInsideShardAreSpread(t *testing.T) {
	sc := test.DefaultClusterBuilder().Build()

	kubeClient, _ := mock.NewDefaultFakeClient(sc)
	shardSpec, memberCluster := createShardSpecAndDefaultCluster(kubeClient, sc)
	firstShardSet := construct.DatabaseStatefulSet(*sc, construct.ShardOptions(0, shardSpec, memberCluster.Name, construct.GetPodEnvOptions()), zap.S())
	secondShardSet := construct.DatabaseStatefulSet(*sc, construct.ShardOptions(1, shardSpec, memberCluster.Name, construct.GetPodEnvOptions()), zap.S())

	assert.Equal(t, sc.ShardRsName(0), firstShardSet.Spec.Selector.MatchLabels[construct.PodAntiAffinityLabelKey])
	assert.Equal(t, sc.ShardRsName(1), secondShardSet.Spec.Selector.MatchLabels[construct.PodAntiAffinityLabelKey])

	firstShartPodAffinityTerm := firstShardSet.Spec.Template.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm
	assert.Equal(t, firstShartPodAffinityTerm.LabelSelector.MatchLabels[construct.PodAntiAffinityLabelKey], sc.ShardRsName(0))

	secondShartPodAffinityTerm := secondShardSet.Spec.Template.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0].PodAffinityTerm
	assert.Equal(t, secondShartPodAffinityTerm.LabelSelector.MatchLabels[construct.PodAntiAffinityLabelKey], sc.ShardRsName(1))
}

func createShardSpecAndDefaultCluster(client kubernetesClient.Client, sc *mdbv1.MongoDB) (*mdbv1.ShardedClusterComponentSpec, multicluster.MemberCluster) {
	shardSpec := sc.Spec.ShardSpec.DeepCopy()
	shardSpec.ClusterSpecList = mdbv1.ClusterSpecList{
		{
			ClusterName: multicluster.LegacyCentralClusterName,
			Members:     sc.Spec.MongodsPerShardCount,
		},
	}

	return shardSpec, multicluster.GetLegacyCentralMemberCluster(sc.Spec.MongodsPerShardCount, 0, client, secrets.SecretClient{KubeClient: client})
}

func createConfigSrvSpec(sc *mdbv1.MongoDB) *mdbv1.ShardedClusterComponentSpec {
	shardSpec := sc.Spec.ConfigSrvSpec.DeepCopy()
	shardSpec.ClusterSpecList = mdbv1.ClusterSpecList{
		{
			ClusterName: multicluster.LegacyCentralClusterName,
			Members:     sc.Spec.MongodsPerShardCount,
		},
	}

	return shardSpec
}

func createMongosSpec(sc *mdbv1.MongoDB) *mdbv1.ShardedClusterComponentSpec {
	shardSpec := sc.Spec.ConfigSrvSpec.DeepCopy()
	shardSpec.ClusterSpecList = mdbv1.ClusterSpecList{
		{
			ClusterName: multicluster.LegacyCentralClusterName,
			Members:     sc.Spec.MongodsPerShardCount,
		},
	}

	return shardSpec
}

func TestShardedCluster_WithTLSEnabled_AndX509Enabled_Succeeds(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().
		EnableTLS().
		EnableX509().
		SetTLSCA("custom-ca").
		Build()

	reconciler, _, clusterClient, _, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	require.NoError(t, err)
	addKubernetesTlsResources(ctx, clusterClient, sc)

	actualResult, err := reconciler.Reconcile(ctx, requestFromObject(sc))
	expectedResult := reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}

	assert.Equal(t, expectedResult, actualResult)
	assert.Nil(t, err)
}

func TestShardedCluster_NeedToPublishState(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().
		EnableTLS().
		SetTLSCA("custom-ca").
		Build()

	// perform successful reconciliation to populate all the stateful sets in the mocked client
	reconciler, reconcilerHelper, clusterClient, _, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	require.NoError(t, err)
	addKubernetesTlsResources(ctx, clusterClient, sc)
	actualResult, err := reconciler.Reconcile(ctx, requestFromObject(sc))
	expectedResult := reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}

	assert.Equal(t, expectedResult, actualResult)
	assert.Nil(t, err)

	allConfigs := reconcilerHelper.getAllConfigs(ctx, *sc, getEmptyDeploymentOptions(), zap.S())

	assert.False(t, anyStatefulSetNeedsToPublishStateToOM(ctx, *sc, clusterClient, reconcilerHelper.deploymentState.LastAchievedSpec, allConfigs, zap.S()))

	// attempting to set tls to false
	require.NoError(t, clusterClient.Get(ctx, kube.ObjectKeyFromApiObject(sc), sc))

	sc.Spec.Security.TLSConfig.Enabled = false

	err = clusterClient.Update(ctx, sc)
	assert.NoError(t, err)

	// Ops Manager state needs to be published first as we want to reach goal state before unmounting certificates
	allConfigs = reconcilerHelper.getAllConfigs(ctx, *sc, getEmptyDeploymentOptions(), zap.S())
	assert.True(t, anyStatefulSetNeedsToPublishStateToOM(ctx, *sc, clusterClient, reconcilerHelper.deploymentState.LastAchievedSpec, allConfigs, zap.S()))
}

func TestShardedCustomPodSpecTemplate(t *testing.T) {
	ctx := context.Background()
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

	sc := test.DefaultClusterBuilder().SetName("pod-spec-sc").EnableTLS().SetTLSCA("custom-ca").
		SetShardPodSpec(corev1.PodTemplateSpec{
			Spec: shardPodSpec,
		}).SetMongosPodSpecTemplate(corev1.PodTemplateSpec{
		Spec: mongosPodSpec,
	}).SetPodConfigSvrSpecTemplate(corev1.PodTemplateSpec{
		Spec: configSrvPodSpec,
	}).Build()

	reconciler, _, kubeClient, _, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	require.NoError(t, err)

	addKubernetesTlsResources(ctx, kubeClient, sc)

	checkReconcileSuccessful(ctx, t, reconciler, sc, kubeClient)

	// read the stateful sets that were created by the operator
	statefulSetSc0, err := kubeClient.GetStatefulSet(ctx, kube.ObjectKey(mock.TestNamespace, "pod-spec-sc-0"))
	assert.NoError(t, err)
	statefulSetSc1, err := kubeClient.GetStatefulSet(ctx, kube.ObjectKey(mock.TestNamespace, "pod-spec-sc-1"))
	assert.NoError(t, err)
	statefulSetScConfig, err := kubeClient.GetStatefulSet(ctx, kube.ObjectKey(mock.TestNamespace, "pod-spec-sc-config"))
	assert.NoError(t, err)
	statefulSetMongoS, err := kubeClient.GetStatefulSet(ctx, kube.ObjectKey(mock.TestNamespace, "pod-spec-sc-mongos"))
	assert.NoError(t, err)

	// assert Pod Spec for Sharded cluster
	assertPodSpecSts(t, &statefulSetSc0, shardPodSpec.NodeName, shardPodSpec.Hostname, shardPodSpec.RestartPolicy)
	assertPodSpecSts(t, &statefulSetSc1, shardPodSpec.NodeName, shardPodSpec.Hostname, shardPodSpec.RestartPolicy)

	// assert Pod Spec for Mongos
	assertPodSpecSts(t, &statefulSetMongoS, mongosPodSpec.NodeName, mongosPodSpec.Hostname, mongosPodSpec.RestartPolicy)

	// assert Pod Spec for ConfigServer
	assertPodSpecSts(t, &statefulSetScConfig, configSrvPodSpec.NodeName, configSrvPodSpec.Hostname, configSrvPodSpec.RestartPolicy)

	podSpecTemplateSc0 := statefulSetSc0.Spec.Template.Spec
	assert.Len(t, podSpecTemplateSc0.Containers, 2, "Should have 3 containers now")
	assert.Equal(t, util.DatabaseContainerName, podSpecTemplateSc0.Containers[0].Name, "Database container should always be first")
	assert.Equal(t, "my-custom-container-sc", podSpecTemplateSc0.Containers[1].Name, "Custom container should be second")

	podSpecTemplateSc1 := statefulSetSc1.Spec.Template.Spec
	assert.Len(t, podSpecTemplateSc1.Containers, 2, "Should have 3 containers now")
	assert.Equal(t, util.DatabaseContainerName, podSpecTemplateSc1.Containers[0].Name, "Database container should always be first")
	assert.Equal(t, "my-custom-container-sc", podSpecTemplateSc1.Containers[1].Name, "Custom container should be second")

	podSpecTemplateMongoS := statefulSetMongoS.Spec.Template.Spec
	assert.Len(t, podSpecTemplateMongoS.Containers, 2, "Should have 3 containers now")
	assert.Equal(t, util.DatabaseContainerName, podSpecTemplateMongoS.Containers[0].Name, "Database container should always be first")
	assert.Equal(t, "my-custom-container-mongos", podSpecTemplateMongoS.Containers[1].Name, "Custom container should be second")

	podSpecTemplateScConfig := statefulSetScConfig.Spec.Template.Spec
	assert.Len(t, podSpecTemplateScConfig.Containers, 2, "Should have 3 containers now")
	assert.Equal(t, util.DatabaseContainerName, podSpecTemplateScConfig.Containers[0].Name, "Database container should always be first")
	assert.Equal(t, "my-custom-container-config", podSpecTemplateScConfig.Containers[1].Name, "Custom container should be second")
}

func TestShardedCustomPodStaticSpecTemplate(t *testing.T) {
	ctx := context.Background()
	t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))
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

	sc := test.DefaultClusterBuilder().SetName("pod-spec-sc").EnableTLS().SetTLSCA("custom-ca").
		SetShardPodSpec(corev1.PodTemplateSpec{
			Spec: shardPodSpec,
		}).SetMongosPodSpecTemplate(corev1.PodTemplateSpec{
		Spec: mongosPodSpec,
	}).SetPodConfigSvrSpecTemplate(corev1.PodTemplateSpec{
		Spec: configSrvPodSpec,
	}).Build()

	reconciler, _, kubeClient, _, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	require.NoError(t, err)

	addKubernetesTlsResources(ctx, kubeClient, sc)

	checkReconcileSuccessful(ctx, t, reconciler, sc, kubeClient)

	// read the stateful sets that were created by the operator
	statefulSetSc0, err := kubeClient.GetStatefulSet(ctx, kube.ObjectKey(mock.TestNamespace, "pod-spec-sc-0"))
	assert.NoError(t, err)
	statefulSetSc1, err := kubeClient.GetStatefulSet(ctx, kube.ObjectKey(mock.TestNamespace, "pod-spec-sc-1"))
	assert.NoError(t, err)
	statefulSetScConfig, err := kubeClient.GetStatefulSet(ctx, kube.ObjectKey(mock.TestNamespace, "pod-spec-sc-config"))
	assert.NoError(t, err)
	statefulSetMongoS, err := kubeClient.GetStatefulSet(ctx, kube.ObjectKey(mock.TestNamespace, "pod-spec-sc-mongos"))
	assert.NoError(t, err)

	// assert Pod Spec for Sharded cluster
	assertPodSpecSts(t, &statefulSetSc0, shardPodSpec.NodeName, shardPodSpec.Hostname, shardPodSpec.RestartPolicy)
	assertPodSpecSts(t, &statefulSetSc1, shardPodSpec.NodeName, shardPodSpec.Hostname, shardPodSpec.RestartPolicy)

	// assert Pod Spec for Mongos
	assertPodSpecSts(t, &statefulSetMongoS, mongosPodSpec.NodeName, mongosPodSpec.Hostname, mongosPodSpec.RestartPolicy)

	// assert Pod Spec for ConfigServer
	assertPodSpecSts(t, &statefulSetScConfig, configSrvPodSpec.NodeName, configSrvPodSpec.Hostname, configSrvPodSpec.RestartPolicy)

	podSpecTemplateSc0 := statefulSetSc0.Spec.Template.Spec
	assert.Len(t, podSpecTemplateSc0.Containers, 4, "Should have 4 containers (3 base + 1 custom)")
	assert.Equal(t, util.AgentContainerName, podSpecTemplateSc0.Containers[0].Name, "Agent container should be first alphabetically")
	assert.Equal(t, "my-custom-container-sc", podSpecTemplateSc0.Containers[len(podSpecTemplateSc0.Containers)-1].Name, "Custom container should be last")

	podSpecTemplateSc1 := statefulSetSc1.Spec.Template.Spec
	assert.Len(t, podSpecTemplateSc1.Containers, 4, "Should have 4 containers (3 base + 1 custom)")
	assert.Equal(t, util.AgentContainerName, podSpecTemplateSc1.Containers[0].Name, "Agent container should be first alphabetically")
	assert.Equal(t, "my-custom-container-sc", podSpecTemplateSc1.Containers[len(podSpecTemplateSc1.Containers)-1].Name, "Custom container should be last")

	podSpecTemplateMongoS := statefulSetMongoS.Spec.Template.Spec
	assert.Len(t, podSpecTemplateMongoS.Containers, 4, "Should have 4 containers (3 base + 1 custom)")
	assert.Equal(t, util.AgentContainerName, podSpecTemplateMongoS.Containers[0].Name, "Agent container should be first alphabetically")
	assert.Equal(t, "my-custom-container-mongos", podSpecTemplateMongoS.Containers[len(podSpecTemplateMongoS.Containers)-1].Name, "Custom container should be last")

	podSpecTemplateScConfig := statefulSetScConfig.Spec.Template.Spec
	assert.Len(t, podSpecTemplateScConfig.Containers, 4, "Should have 4 containers (3 base + 1 custom)")
	assert.Equal(t, util.AgentContainerName, podSpecTemplateScConfig.Containers[0].Name, "Agent container should be first alphabetically")
	assert.Equal(t, "my-custom-container-config", podSpecTemplateScConfig.Containers[len(podSpecTemplateScConfig.Containers)-1].Name, "Custom container should be last")
}

func TestFeatureControlsNoAuth(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().RemoveAuth().Build()
	omConnectionFactory := om.NewCachedOMConnectionFactory(omConnectionFactoryFuncSettingVersion())
	fakeClient := mock.NewDefaultFakeClientWithOMConnectionFactory(omConnectionFactory, sc)
	reconciler := newShardedClusterReconciler(ctx, fakeClient, nil, "fake-initDatabaseNonStaticImageVersion", "fake-databaseNonStaticImageVersion", false, false, nil, omConnectionFactory.GetConnectionFunc)

	checkReconcileSuccessful(ctx, t, reconciler, sc, fakeClient)

	cf, _ := omConnectionFactory.GetConnection().GetControlledFeature()

	assert.Len(t, cf.Policies, 2)

	assert.Equal(t, cf.ManagementSystem.Version, util.OperatorVersion)
	assert.Equal(t, cf.ManagementSystem.Name, util.OperatorName)
	assert.Equal(t, cf.Policies[0].PolicyType, controlledfeature.ExternallyManaged)
	assert.Equal(t, cf.Policies[1].PolicyType, controlledfeature.DisableMongodVersion)
	assert.Len(t, cf.Policies[0].DisabledParams, 0)
}

func TestScalingShardedCluster_ScalesOneMemberAtATime_WhenScalingUp(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().
		SetMongodsPerShardCountSpec(3).
		SetMongodsPerShardCountStatus(0).
		SetConfigServerCountSpec(1).
		SetConfigServerCountStatus(0).
		SetMongosCountSpec(1).
		SetMongosCountStatus(0).
		SetShardCountSpec(1).
		SetShardCountStatus(0).
		Build()

	clusterClient, omConnectionFactory := mock.NewDefaultFakeClient(sc)
	reconciler, _, err := newShardedClusterReconcilerFromResource(ctx, nil, "", "", sc, nil, clusterClient, omConnectionFactory)
	require.NoError(t, err)

	// perform initial reconciliation, so we are not creating a new resource
	checkReconcileSuccessful(ctx, t, reconciler, sc, clusterClient)

	getShard := func(i int) appsv1.StatefulSet {
		sts := appsv1.StatefulSet{}
		err := clusterClient.Get(ctx, types.NamespacedName{Name: sc.ShardRsName(i), Namespace: sc.Namespace}, &sts)
		assert.NoError(t, err)
		return sts
	}

	assert.Equal(t, 1, sc.Status.MongosCount)
	assert.Equal(t, 1, sc.Status.ConfigServerCount)
	require.Equal(t, 1, sc.Status.ShardCount)
	assert.Equal(t, int32(3), *getShard(0).Spec.Replicas)

	// Scale up the Sharded Cluster
	sc.Spec.MongodsPerShardCount = 6
	sc.Spec.MongosCount = 3
	sc.Spec.ShardCount = 2
	sc.Spec.ConfigServerCount = 2

	err = clusterClient.Update(ctx, sc)
	assert.NoError(t, err)

	var deployment om.Deployment
	performReconciliation := func(shouldRetry bool) {
		res, err := reconciler.Reconcile(ctx, requestFromObject(sc))
		assert.NoError(t, err)
		if shouldRetry {
			assert.Equal(t, time.Duration(10000000000), res.RequeueAfter)
		} else {
			ok, _ := workflow.OK().ReconcileResult()
			assert.Equal(t, ok, res)
		}
		err = clusterClient.Get(ctx, sc.ObjectKey(), sc)
		assert.NoError(t, err)

		deployment, err = omConnectionFactory.GetConnection().ReadDeployment()
		assert.NoError(t, err)
	}

	t.Run("1st reconciliation", func(t *testing.T) {
		performReconciliation(true)

		assert.Equal(t, 2, sc.Status.MongosCount)
		assert.Equal(t, 2, sc.Status.ConfigServerCount)
		// Shard 0 is scaling one replica at a time
		assert.Equal(t, int32(4), *getShard(0).Spec.Replicas)
		// Shard 1 is scaling for the first time (new shard), we create it directly with the target number of replicas
		assert.Equal(t, int32(6), *getShard(1).Spec.Replicas)
		assert.Len(t, deployment.GetAllProcessNames(), 14)
	})

	t.Run("2nd reconciliation", func(t *testing.T) {
		performReconciliation(true)
		assert.Equal(t, 3, sc.Status.MongosCount)
		assert.Equal(t, 2, sc.Status.ConfigServerCount)
		// Shard 0 is scaling one replica at a time
		assert.Equal(t, int32(5), *getShard(0).Spec.Replicas)
		assert.Equal(t, int32(6), *getShard(1).Spec.Replicas)
		assert.Len(t, deployment.GetAllProcessNames(), 16)
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
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().
		SetMongodsPerShardCountSpec(6).
		SetMongodsPerShardCountStatus(6).
		SetConfigServerCountSpec(3).
		SetConfigServerCountStatus(3).
		SetMongosCountSpec(3).
		SetMongosCountStatus(3).
		SetShardCountSpec(3).
		SetShardCountStatus(3).
		Build()

	reconciler, _, clusterClient, _, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	require.NoError(t, err)
	// perform initial reconciliation so we are not creating a new resource
	checkReconcileSuccessful(ctx, t, reconciler, sc, clusterClient)

	err = clusterClient.Get(ctx, sc.ObjectKey(), sc)
	assert.NoError(t, err)

	assert.Equal(t, 3, sc.Status.ShardCount)
	assert.Equal(t, 3, sc.Status.ConfigServerCount)
	assert.Equal(t, 3, sc.Status.MongosCount)
	assert.Equal(t, 6, sc.Status.MongodsPerShardCount)

	// Scale up the Sharded Cluster
	sc.Spec.MongodsPerShardCount = 3 // from 6
	sc.Spec.MongosCount = 1          // from 3
	sc.Spec.ShardCount = 1           // from 2
	sc.Spec.ConfigServerCount = 1    // from 3

	err = clusterClient.Update(ctx, sc)
	assert.NoError(t, err)

	performReconciliation := func(shouldRetry bool) {
		res, err := reconciler.Reconcile(ctx, requestFromObject(sc))
		assert.NoError(t, err)
		if shouldRetry {
			assert.Equal(t, time.Duration(10000000000), res.RequeueAfter)
		} else {
			ok, _ := workflow.OK().ReconcileResult()
			assert.Equal(t, ok, res)
		}
		err = clusterClient.Get(ctx, sc.ObjectKey(), sc)
		assert.NoError(t, err)
	}

	getShard := func(i int) *appsv1.StatefulSet {
		sts := appsv1.StatefulSet{}
		err := clusterClient.Get(ctx, types.NamespacedName{Name: sc.ShardRsName(i), Namespace: sc.Namespace}, &sts)
		if errors.IsNotFound(err) {
			return nil
		}
		return &sts
	}

	t.Run("1st reconciliation", func(t *testing.T) {
		performReconciliation(true)
		assert.Equal(t, 3, sc.Status.ShardCount)
		assert.Equal(t, 2, sc.Status.MongosCount)
		assert.Equal(t, 2, sc.Status.ConfigServerCount)
		assert.Equal(t, int32(5), *getShard(0).Spec.Replicas)
		// shards to be deleted are not updated anymore
		assert.Equal(t, int32(6), *getShard(1).Spec.Replicas)
		assert.Equal(t, int32(6), *getShard(2).Spec.Replicas)
		assert.NotNil(t, getShard(1), "Shard 1 should not be removed until the scaling operation is complete")
		assert.NotNil(t, getShard(2), "Shard 2 should not be removed until the scaling operation is complete")
	})
	t.Run("2nd reconciliation", func(t *testing.T) {
		performReconciliation(true)
		assert.Equal(t, 3, sc.Status.ShardCount)
		assert.Equal(t, 1, sc.Status.MongosCount)
		assert.Equal(t, 1, sc.Status.ConfigServerCount)
		assert.Equal(t, int32(4), *getShard(0).Spec.Replicas)
		assert.Equal(t, int32(6), *getShard(1).Spec.Replicas)
		assert.Equal(t, int32(6), *getShard(2).Spec.Replicas)
		assert.NotNil(t, getShard(1), "Shard 1 should not be removed until the scaling operation is complete")
		assert.NotNil(t, getShard(2), "Shard 2 should not be removed until the scaling operation is complete")
	})
	t.Run("Final reconciliation", func(t *testing.T) {
		performReconciliation(false)
		assert.Equal(t, 1, sc.Status.ShardCount, "Upon finishing reconciliation, the original shard count should be set to the current value")
		assert.Equal(t, 1, sc.Status.MongosCount)
		assert.Equal(t, 1, sc.Status.ConfigServerCount)
		assert.Equal(t, int32(3), *getShard(0).Spec.Replicas)
		assert.Nil(t, getShard(1), "Shard 1 should be removed as we have reached have finished scaling")
		assert.Nil(t, getShard(2), "Shard 2 should be removed as we have reached have finished scaling")
	})
}

func TestFeatureControlsAuthEnabled(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().Build()
	omConnectionFactory := om.NewCachedOMConnectionFactory(omConnectionFactoryFuncSettingVersion())
	fakeClient := mock.NewDefaultFakeClientWithOMConnectionFactory(omConnectionFactory, sc)
	reconciler := newShardedClusterReconciler(ctx, fakeClient, nil, "fake-initDatabaseNonStaticImageVersion", "fake-databaseNonStaticImageVersion", false, false, nil, omConnectionFactory.GetConnectionFunc)

	checkReconcileSuccessful(ctx, t, reconciler, sc, fakeClient)

	cf, _ := omConnectionFactory.GetConnection().GetControlledFeature()

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
	ctx := context.Background()
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

	reconciler, _, clusterClient, _, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	require.NoError(t, err)

	checkReconcileSuccessful(ctx, t, reconciler, sc, clusterClient)

	t.Run("Config Server Port is configured", func(t *testing.T) {
		configSrvSvc, err := clusterClient.GetService(ctx, kube.ObjectKey(sc.Namespace, sc.ConfigSrvServiceName()))
		assert.NoError(t, err)
		assert.Equal(t, int32(30000), configSrvSvc.Spec.Ports[0].Port)
	})

	t.Run("Mongos Port is configured", func(t *testing.T) {
		mongosSvc, err := clusterClient.GetService(ctx, kube.ObjectKey(sc.Namespace, sc.ServiceName()))
		assert.NoError(t, err)
		assert.Equal(t, int32(30001), mongosSvc.Spec.Ports[0].Port)
	})

	t.Run("Shard Port is configured", func(t *testing.T) {
		shardSvc, err := clusterClient.GetService(ctx, kube.ObjectKey(sc.Namespace, sc.ShardServiceName()))
		assert.NoError(t, err)
		assert.Equal(t, int32(30002), shardSvc.Spec.Ports[0].Port)
	})
}

// TestShardedCluster_ConfigMapAndSecretWatched verifies that config map and secret are added to the internal
// map that allows to watch them for changes
func TestShardedCluster_ConfigMapAndSecretWatched(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().Build()

	reconciler, _, clusterClient, _, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	require.NoError(t, err)

	checkReconcileSuccessful(ctx, t, reconciler, sc, clusterClient)

	expected := map[watch.Object][]types.NamespacedName{
		{ResourceType: watch.ConfigMap, Resource: kube.ObjectKey(mock.TestNamespace, mock.TestProjectConfigMapName)}: {kube.ObjectKey(mock.TestNamespace, sc.Name)},
		{ResourceType: watch.Secret, Resource: kube.ObjectKey(mock.TestNamespace, sc.Spec.Credentials)}:              {kube.ObjectKey(mock.TestNamespace, sc.Name)},
	}

	assert.Equal(t, reconciler.resourceWatcher.GetWatchedResources(), expected)
}

// TestShardedClusterTLSResourcesWatched verifies that TLS config map and secret are added to the internal
// map that allows to watch them for changes
func TestShardedClusterTLSAndInternalAuthResourcesWatched(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().SetShardCountSpec(1).EnableTLS().SetTLSCA("custom-ca").Build()
	sc.Spec.Security.Authentication.InternalCluster = "x509"
	reconciler, _, clusterClient, _, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	require.NoError(t, err)

	addKubernetesTlsResources(ctx, clusterClient, sc)
	checkReconcileSuccessful(ctx, t, reconciler, sc, clusterClient)

	expectedWatchedResources := []watch.Object{
		getWatch(sc.Namespace, sc.Name+"-config-cert", watch.Secret),
		getWatch(sc.Namespace, sc.Name+"-config-clusterfile", watch.Secret),
		getWatch(sc.Namespace, sc.Name+"-mongos-cert", watch.Secret),
		getWatch(sc.Namespace, sc.Name+"-mongos-clusterfile", watch.Secret),
		getWatch(sc.Namespace, sc.Name+"-0-cert", watch.Secret),
		getWatch(sc.Namespace, sc.Name+"-0-clusterfile", watch.Secret),
		getWatch(sc.Namespace, "custom-ca", watch.ConfigMap),
		getWatch(sc.Namespace, "my-credentials", watch.Secret),
		getWatch(sc.Namespace, "my-project", watch.ConfigMap),
	}

	var actual []watch.Object
	for obj := range reconciler.resourceWatcher.GetWatchedResources() {
		actual = append(actual, obj)
	}

	assert.ElementsMatch(t, expectedWatchedResources, actual)

	// ReconcileMongoDbShardedCluster.publishDeployment - once internal cluster authentication is enabled,
	// it is impossible to turn it off.
	sc.Spec.Security.TLSConfig.Enabled = false
	sc.Spec.Security.Authentication.InternalCluster = ""
	err = clusterClient.Update(ctx, sc)
	assert.NoError(t, err)

	res, err := reconciler.Reconcile(ctx, requestFromObject(sc))
	assert.Equal(t, reconcile.Result{RequeueAfter: 10 * time.Second}, res)
	assert.NoError(t, err)
	assert.Len(t, reconciler.resourceWatcher.GetWatchedResources(), 2)
}

func TestBackupConfiguration_ShardedCluster(t *testing.T) {
	ctx := context.Background()
	sc := mdbv1.NewClusterBuilder().
		SetNamespace(mock.TestNamespace).
		SetConnectionSpec(testConnectionSpec()).
		SetBackup(mdbv1.Backup{
			Mode: "enabled",
		}).
		Build()

	reconciler, _, clusterClient, omConnectionFactory, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	require.NoError(t, err)
	omConnectionFactory.SetPostCreateHook(func(c om.Connection) {
		// 4 because config server + num shards + 1 for entity to represent the sharded cluster itself
		clusterIds := []string{"1", "2", "3", "4"}
		typeNames := []string{"SHARDED_REPLICA_SET", "REPLICA_SET", "REPLICA_SET", "CONFIG_SERVER_REPLICA_SET"}
		for i, clusterId := range clusterIds {
			_, err := c.UpdateBackupConfig(&backup.Config{
				ClusterId: clusterId,
				Status:    backup.Inactive,
			})
			require.NoError(t, err)

			c.(*om.MockedOmConnection).BackupHostClusters[clusterId] = &backup.HostCluster{
				ClusterName: sc.Name,
				ShardName:   "ShardedCluster",
				TypeName:    typeNames[i],
			}
			c.(*om.MockedOmConnection).CleanHistory()
		}
	})

	assertAllOtherBackupConfigsRemainUntouched := func(t *testing.T) {
		for _, configId := range []string{"2", "3", "4"} {
			config, err := omConnectionFactory.GetConnection().ReadBackupConfig(configId)
			assert.NoError(t, err)
			// backup status should remain INACTIVE for all non "SHARDED_REPLICA_SET" configs.
			assert.Equal(t, backup.Inactive, config.Status)
		}
	}

	t.Run("Backup can be started", func(t *testing.T) {
		checkReconcileSuccessful(ctx, t, reconciler, sc, clusterClient)

		config, err := omConnectionFactory.GetConnection().ReadBackupConfig("1")
		assert.NoError(t, err)
		assert.Equal(t, backup.Started, config.Status)
		assert.Equal(t, "1", config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
		assertAllOtherBackupConfigsRemainUntouched(t)
	})

	t.Run("Backup snapshot schedule tests", backupSnapshotScheduleTests(sc, clusterClient, reconciler, omConnectionFactory, "1"))

	t.Run("Backup can be stopped", func(t *testing.T) {
		sc.Spec.Backup.Mode = "disabled"
		err := clusterClient.Update(ctx, sc)
		assert.NoError(t, err)

		checkReconcileSuccessful(ctx, t, reconciler, sc, clusterClient)

		config, err := omConnectionFactory.GetConnection().ReadBackupConfig("1")
		assert.NoError(t, err)
		assert.Equal(t, backup.Stopped, config.Status)
		assert.Equal(t, "1", config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
		assertAllOtherBackupConfigsRemainUntouched(t)
	})

	t.Run("Backup can be terminated", func(t *testing.T) {
		sc.Spec.Backup.Mode = "terminated"
		err := clusterClient.Update(ctx, sc)
		assert.NoError(t, err)

		checkReconcileSuccessful(ctx, t, reconciler, sc, clusterClient)

		config, err := omConnectionFactory.GetConnection().ReadBackupConfig("1")
		assert.NoError(t, err)
		assert.Equal(t, backup.Terminating, config.Status)
		assert.Equal(t, "1", config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
		assertAllOtherBackupConfigsRemainUntouched(t)
	})
}

// createShardedClusterTLSSecretsFromCustomCerts creates and populates all the required
// secrets required to enabled TLS with custom certs for all sharded cluster components.
func createShardedClusterTLSSecretsFromCustomCerts(ctx context.Context, sc *mdbv1.MongoDB, prefix string, client kubernetesClient.Client) {
	mongosSecret := secret.Builder().
		SetName(fmt.Sprintf("%s-%s-cert", prefix, sc.MongosRsName())).
		SetNamespace(sc.Namespace).SetDataType(corev1.SecretTypeTLS).
		Build()

	mongosSecret.Data["tls.crt"], mongosSecret.Data["tls.key"] = createMockCertAndKeyBytes()

	err := client.CreateSecret(ctx, mongosSecret)
	if err != nil {
		panic(err)
	}

	configSrvSecret := secret.Builder().
		SetName(fmt.Sprintf("%s-%s-cert", prefix, sc.ConfigRsName())).
		SetNamespace(sc.Namespace).SetDataType(corev1.SecretTypeTLS).
		Build()

	configSrvSecret.Data["tls.crt"], configSrvSecret.Data["tls.key"] = createMockCertAndKeyBytes()

	err = client.CreateSecret(ctx, configSrvSecret)
	if err != nil {
		panic(err)
	}

	for i := 0; i < sc.Spec.ShardCount; i++ {
		shardSecret := secret.Builder().
			SetName(fmt.Sprintf("%s-%s-cert", prefix, sc.ShardRsName(i))).
			SetNamespace(sc.Namespace).SetDataType(corev1.SecretTypeTLS).
			Build()

		shardSecret.Data["tls.crt"], shardSecret.Data["tls.key"] = createMockCertAndKeyBytes()

		err = client.CreateSecret(ctx, shardSecret)
		if err != nil {
			panic(err)
		}
	}
}

func TestTlsConfigPrefix_ForShardedCluster(t *testing.T) {
	ctx := context.Background()
	sc := test.DefaultClusterBuilder().
		SetTLSConfig(mdbv1.TLSConfig{
			Enabled: false,
		}).
		Build()

	reconciler, _, clusterClient, _, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	require.NoError(t, err)

	createShardedClusterTLSSecretsFromCustomCerts(ctx, sc, "my-prefix", clusterClient)

	checkReconcileSuccessful(ctx, t, reconciler, sc, clusterClient)
}

func TestShardSpecificPodSpec(t *testing.T) {
	ctx := context.Background()
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

	sc := test.DefaultClusterBuilder().SetName("shard-specific-pod-spec").EnableTLS().SetTLSCA("custom-ca").
		SetShardPodSpec(corev1.PodTemplateSpec{
			Spec: shardPodSpec,
		}).SetShardSpecificPodSpecTemplate([]corev1.PodTemplateSpec{
		{
			Spec: shard0PodSpec,
		},
	}).Build()

	reconciler, _, clusterClient, _, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
	require.NoError(t, err)
	addKubernetesTlsResources(ctx, clusterClient, sc)
	checkReconcileSuccessful(ctx, t, reconciler, sc, clusterClient)

	// read the statefulsets from the cluster
	statefulSetSc0, err := clusterClient.GetStatefulSet(ctx, kube.ObjectKey(mock.TestNamespace, "shard-specific-pod-spec-0"))
	assert.NoError(t, err)
	statefulSetSc1, err := clusterClient.GetStatefulSet(ctx, kube.ObjectKey(mock.TestNamespace, "shard-specific-pod-spec-1"))
	assert.NoError(t, err)

	// shard0 should have the override
	assertPodSpecSts(t, &statefulSetSc0, shard0PodSpec.NodeName, shard0PodSpec.Hostname, shard0PodSpec.RestartPolicy)

	// shard1 should have the common one
	assertPodSpecSts(t, &statefulSetSc1, shardPodSpec.NodeName, shardPodSpec.Hostname, shardPodSpec.RestartPolicy)
}

func TestShardedClusterAgentVersionMapping(t *testing.T) {
	ctx := context.Background()
	defaultResource := test.DefaultClusterBuilder().Build()
	reconcilerFactory := func(sc *mdbv1.MongoDB) (reconcile.Reconciler, kubernetesClient.Client) {
		// Go couldn't infer correctly that *ReconcileMongoDbShardedCluster implemented *reconciler.Reconciler interface
		// without this anonymous function
		reconciler, _, mockClient, _, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
		require.NoError(t, err)
		return reconciler, mockClient
	}

	defaultResources := testReconciliationResources{
		Resource:          defaultResource,
		ReconcilerFactory: reconcilerFactory,
	}

	containers := []corev1.Container{{Name: util.AgentContainerName, Image: "foo"}}
	podTemplate := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: containers,
		},
	}

	// Override each sts of the sharded cluster
	overridenResource := test.DefaultClusterBuilder().SetMongosPodSpecTemplate(podTemplate).SetPodConfigSvrSpecTemplate(podTemplate).SetShardPodSpec(podTemplate).Build()
	overridenResources := testReconciliationResources{
		Resource:          overridenResource,
		ReconcilerFactory: reconcilerFactory,
	}

	agentVersionMappingTest(ctx, t, defaultResources, overridenResources)
}

func assertPodSpecSts(t *testing.T, sts *appsv1.StatefulSet, nodeName, hostName string, restartPolicy corev1.RestartPolicy) {
	podSpecTemplate := sts.Spec.Template.Spec
	// ensure values were passed to the stateful set
	assert.Equal(t, nodeName, podSpecTemplate.NodeName)
	assert.Equal(t, hostName, podSpecTemplate.Hostname)
	assert.Equal(t, restartPolicy, podSpecTemplate.RestartPolicy)

	if architectures.IsRunningStaticArchitecture(nil) {
		assert.Equal(t, util.AgentContainerName, podSpecTemplate.Containers[0].Name, "Database container should always be first")
	} else {
		assert.Equal(t, util.DatabaseContainerName, podSpecTemplate.Containers[0].Name, "Database container should always be first")
		assert.True(t, statefulset.VolumeMountWithNameExists(podSpecTemplate.Containers[0].VolumeMounts, construct.PvcNameDatabaseScripts))
	}
}

func createMongosProcesses(mongoDBImage string, forceEnterprise bool, set appsv1.StatefulSet, mdb *mdbv1.MongoDB, certificateFilePath string) []om.Process {
	hostnames, names := dns.GetDnsForStatefulSet(set, mdb.Spec.GetClusterDomain(), nil)
	processes := make([]om.Process, len(hostnames))

	for idx, hostname := range hostnames {
		processes[idx] = om.NewMongosProcess(names[idx], hostname, mongoDBImage, forceEnterprise, mdb.Spec.MongosSpec.GetAdditionalMongodConfig(), mdb.GetSpec(), certificateFilePath, mdb.Annotations, mdb.CalculateFeatureCompatibilityVersion())
	}

	return processes
}

func createDeploymentFromShardedCluster(t *testing.T, updatable v1.CustomResourceReadWriter) om.Deployment {
	sh := updatable.(*mdbv1.MongoDB)

	shards := make([]om.ReplicaSetWithProcesses, sh.Spec.ShardCount)
	kubeClient, _ := mock.NewDefaultFakeClient(sh)
	shardSpec, memberCluster := createShardSpecAndDefaultCluster(kubeClient, sh)

	for i := 0; i < sh.Spec.ShardCount; i++ {
		shardOptions := construct.ShardOptions(i, shardSpec, memberCluster.Name,
			Replicas(sh.Spec.MongodsPerShardCount),
			construct.GetPodEnvOptions(),
		)
		shardSts := construct.DatabaseStatefulSet(*sh, shardOptions, zap.S())
		shards[i], _ = buildReplicaSetFromProcesses(shardSts.Name, createShardProcesses("fake-mongoDBImage", false, shardSts, sh, ""), sh, sh.Spec.GetMemberOptions(), om.NewDeployment())
	}

	desiredMongosConfig := createMongosSpec(sh)
	mongosOptions := construct.MongosOptions(
		desiredMongosConfig,
		memberCluster.Name,
		Replicas(sh.Spec.MongosCount),
		construct.GetPodEnvOptions(),
	)
	mongosSts := construct.DatabaseStatefulSet(*sh, mongosOptions, zap.S())
	mongosProcesses := createMongosProcesses("fake-mongoDBImage", false, mongosSts, sh, util.PEMKeyFilePathInContainer)

	desiredConfigSrvConfig := createConfigSrvSpec(sh)
	configServerOptions := construct.ConfigServerOptions(
		desiredConfigSrvConfig,
		memberCluster.Name,
		Replicas(sh.Spec.ConfigServerCount),
		construct.GetPodEnvOptions(),
	)
	configSvrSts := construct.DatabaseStatefulSet(*sh, configServerOptions, zap.S())
	configRs, _ := buildReplicaSetFromProcesses(configSvrSts.Name, createConfigSrvProcesses("fake-mongoDBImage", false, configSvrSts, sh, ""), sh, sh.Spec.GetMemberOptions(), om.NewDeployment())

	d := om.NewDeployment()
	_, err := d.MergeShardedCluster(om.DeploymentShardedClusterMergeOptions{
		Name:            sh.Name,
		MongosProcesses: mongosProcesses,
		ConfigServerRs:  configRs,
		Shards:          shards,
		Finalizing:      false,
	})
	assert.NoError(t, err)
	d.AddMonitoringAndBackup(zap.S(), sh.Spec.GetSecurity().IsTLSEnabled(), util.CAFilePathInContainer)
	return d
}

func defaultClusterReconciler(ctx context.Context, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, sc *mdbv1.MongoDB, globalMemberClustersMap map[string]client.Client) (*ReconcileMongoDbShardedCluster, *ShardedClusterReconcileHelper, kubernetesClient.Client, *om.CachedOMConnectionFactory, error) {
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(sc)
	r, reconcileHelper, err := newShardedClusterReconcilerFromResource(ctx, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, sc, globalMemberClustersMap, kubeClient, omConnectionFactory)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return r, reconcileHelper, kubeClient, omConnectionFactory, nil
}

func newShardedClusterReconcilerFromResource(ctx context.Context, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, sc *mdbv1.MongoDB, globalMemberClustersMap map[string]client.Client, kubeClient kubernetesClient.Client, omConnectionFactory *om.CachedOMConnectionFactory) (*ReconcileMongoDbShardedCluster, *ShardedClusterReconcileHelper, error) {
	r := newShardedClusterReconciler(ctx, kubeClient, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, false, false, globalMemberClustersMap, omConnectionFactory.GetConnectionFunc)
	reconcileHelper, err := NewShardedClusterReconcilerHelper(ctx, r.ReconcileCommonController, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, false, false, sc, globalMemberClustersMap, omConnectionFactory.GetConnectionFunc, zap.S())
	if err != nil {
		return nil, nil, err
	}
	if err := kubeClient.Get(ctx, kube.ObjectKeyFromApiObject(sc), sc); err != nil {
		return nil, nil, err
	}
	return r, reconcileHelper, nil
}

func computeSingleClusterShardOverridesFromDistribution(shardOverridesDistribution map[string]int) []mdbv1.ShardOverride {
	var shardOverrides []mdbv1.ShardOverride

	// This will create shard overrides for shards references by shardNames, shardCount can be greater than the length
	// of the map
	for shardName, memberCount := range shardOverridesDistribution {
		// Construct the ShardOverride for that shard
		shardOverride := mdbv1.ShardOverride{
			ShardNames: []string{shardName},
			Members:    ptr.To(memberCount),
		}

		// Append the constructed ShardOverride to the shardOverrides slice
		shardOverrides = append(shardOverrides, shardOverride)
	}
	return shardOverrides
}

type SingleClusterShardedScalingTestCase struct {
	name         string
	scalingSteps []SingleClusterShardedScalingStep
}

type SingleClusterShardedScalingStep struct {
	name                      string
	shardCount                int
	mongodsPerShardCount      int
	shardOverrides            map[string]int
	expectedShardDistribution []int
}

// This scaling test simulates multiple reconciliation loops until the reconciliation succeeds.
// We add hostnames to the OM mock so that the operator sees them as ready, and we mark the sts ready in the kube client
// as well. Because we mark all hostnames in OM ready at the beginning of the test, there are edge cases where we don't
// catch errors.
func SingleClusterShardedScalingWithOverridesTestCase(t *testing.T, tc SingleClusterShardedScalingTestCase) {
	ctx := context.Background()

	mongosCount := 1
	configSrvCount := 1

	sc := test.DefaultClusterBuilder().
		SetTopology(mdbv1.ClusterTopologySingleCluster).
		SetShardCountSpec(tc.scalingSteps[0].shardCount).
		SetMongodsPerShardCountSpec(tc.scalingSteps[0].mongodsPerShardCount).
		SetConfigServerCountSpec(configSrvCount).
		SetMongosCountSpec(mongosCount).
		SetShardOverrides(computeSingleClusterShardOverridesFromDistribution(tc.scalingSteps[0].shardOverrides)).
		Build()

	sc.Status = mdbv1.MongoDbStatus{} // The default builder fill scaling status with incorrect values by default, TODO change the builder

	// We add these hosts so that they are available in the mocked OM connection
	addAllHostsWithDistribution := func(connection om.Connection, mongosCount int, configSrvCount int, shardDistribution []map[string]int) {
		var shardMemberCounts []int
		for _, distribution := range shardDistribution {
			memberCount := distribution[multicluster.LegacyCentralClusterName]
			shardMemberCounts = append(shardMemberCounts, memberCount)
		}
		allHostnames, _ := generateAllHostsSingleCluster(sc, mongosCount, configSrvCount, shardMemberCounts, test.ClusterLocalDomains, test.NoneExternalClusterDomains)
		connection.(*om.MockedOmConnection).AddHosts(allHostnames)
	}

	for _, scalingStep := range tc.scalingSteps {
		t.Run(scalingStep.name, func(t *testing.T) {
			reconciler, reconcilerHelper, kubeClient, omConnectionFactory, err := defaultClusterReconciler(ctx, nil, "", "", sc, nil)
			_ = omConnectionFactory.GetConnectionFunc(&om.OMContext{GroupName: om.TestGroupName})
			require.NoError(t, err)
			clusterMapping := reconcilerHelper.deploymentState.ClusterMapping

			var expectedShardDistribution []map[string]int
			for _, memberCount := range scalingStep.expectedShardDistribution {
				expectedShardDistribution = append(expectedShardDistribution, map[string]int{multicluster.LegacyCentralClusterName: memberCount})
			}

			addAllHostsWithDistribution(omConnectionFactory.GetConnection(), mongosCount, configSrvCount, expectedShardDistribution)

			err = kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(sc), sc)
			require.NoError(t, err)
			sc.Spec.MongodsPerShardCount = scalingStep.mongodsPerShardCount
			sc.Spec.ShardCount = scalingStep.shardCount
			sc.Spec.ShardOverrides = computeSingleClusterShardOverridesFromDistribution(scalingStep.shardOverrides)
			err = kubeClient.Update(ctx, sc)
			require.NoError(t, err)
			reconcileUntilSuccessful(ctx, t, reconciler, sc, kubeClient, []client.Client{kubeClient}, nil, false)

			// Verify scaled deployment
			checkCorrectShardDistributionInStatefulSets(t, ctx, sc, clusterMapping, map[string]client.Client{multicluster.LegacyCentralClusterName: kubeClient}, expectedShardDistribution)
		})
	}
}

func TestSingleClusterShardedScalingWithOverrides(t *testing.T) {
	scDefaultName := test.SCBuilderDefaultName + "-"
	testCases := []SingleClusterShardedScalingTestCase{
		{
			name: "Basic sample test",
			scalingSteps: []SingleClusterShardedScalingStep{
				{
					name:                 "Initial scaling",
					shardCount:           3,
					mongodsPerShardCount: 3,
					shardOverrides: map[string]int{
						scDefaultName + "0": 5,
					},
					expectedShardDistribution: []int{
						5,
						3,
						3,
					},
				},
				{
					name:                 "Scale up mongodsPerShard",
					shardCount:           3,
					mongodsPerShardCount: 5,
					shardOverrides: map[string]int{
						scDefaultName + "0": 5,
					},
					expectedShardDistribution: []int{
						5,
						5,
						5,
					},
				},
			},
		},
		{
			// This operation works in unit test only
			// In e2e tests, the operator is waiting for uncreated hostnames to be ready
			name: "Scale overrides up and down",
			scalingSteps: []SingleClusterShardedScalingStep{
				{
					name:                 "Initial deployment",
					shardCount:           4,
					mongodsPerShardCount: 2,
					shardOverrides: map[string]int{
						scDefaultName + "0": 3,
						scDefaultName + "1": 3,
						scDefaultName + "3": 2,
					},
					expectedShardDistribution: []int{
						3,
						3,
						2, // Not overridden
						2,
					},
				},
				{
					name:                 "Scale overrides",
					shardCount:           4,
					mongodsPerShardCount: 2,
					shardOverrides: map[string]int{
						scDefaultName + "0": 2, // Scaled down
						scDefaultName + "1": 2, // Scaled down
						scDefaultName + "3": 3, // Scaled up
					},
					expectedShardDistribution: []int{
						2, // Scaled down
						2, // Scaled down
						2, // Not overridden
						3, // Scaled up
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			SingleClusterShardedScalingWithOverridesTestCase(t, tc)
		})
	}
}

func generateHostsWithDistributionSingleCluster(stsName string, namespace string, memberCount int, clusterDomain string, externalClusterDomain string) ([]string, []string) {
	var hosts []string
	var podNames []string
	for podIdx := range memberCount {
		hosts = append(hosts, getSingleClusterFQDN(stsName, namespace, podIdx, clusterDomain, externalClusterDomain))
		podNames = append(podNames, getPodNameSingleCluster(stsName, podIdx))
	}

	return podNames, hosts
}

func getPodNameSingleCluster(stsName string, podIdx int) string {
	return fmt.Sprintf("%s-%d", stsName, podIdx)
}

func getSingleClusterFQDN(stsName string, namespace string, podIdx int, clusterDomain string, externalClusterDomain string) string {
	if len(externalClusterDomain) != 0 {
		return fmt.Sprintf("%s.%s", getPodNameSingleCluster(stsName, podIdx), externalClusterDomain)
	}
	return fmt.Sprintf("%s-svc.%s.svc.%s", getPodNameSingleCluster(stsName, podIdx), namespace, clusterDomain)
}

func generateAllHostsSingleCluster(sc *mdbv1.MongoDB, mongosCount int, configSrvCount int, shardsMemberCounts []int, clusterDomain test.ClusterDomains, externalClusterDomain test.ClusterDomains) ([]string, []string) {
	var allHosts []string
	var allPodNames []string
	podNames, hosts := generateHostsWithDistributionSingleCluster(sc.MongosRsName(), sc.Namespace, mongosCount, clusterDomain.MongosExternalDomain, externalClusterDomain.MongosExternalDomain)
	allHosts = append(allHosts, hosts...)
	allPodNames = append(allPodNames, podNames...)

	podNames, hosts = generateHostsWithDistributionSingleCluster(sc.ConfigRsName(), sc.Namespace, configSrvCount, clusterDomain.ConfigServerExternalDomain, externalClusterDomain.ConfigServerExternalDomain)
	allHosts = append(allHosts, hosts...)
	allPodNames = append(allPodNames, podNames...)

	for shardIdx := 0; shardIdx < sc.Spec.ShardCount; shardIdx++ {
		podNames, hosts = generateHostsWithDistributionSingleCluster(sc.ShardRsName(shardIdx), sc.Namespace, shardsMemberCounts[shardIdx], clusterDomain.ShardsExternalDomain, externalClusterDomain.ShardsExternalDomain)
		allHosts = append(allHosts, hosts...)
		allPodNames = append(allPodNames, podNames...)
	}
	return allHosts, allPodNames
}
