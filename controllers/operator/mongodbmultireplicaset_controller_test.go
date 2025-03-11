package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "github.com/mongodb/mongodb-kubernetes-operator/api/v1"
	mcoConstruct "github.com/mongodb/mongodb-kubernetes-operator/controllers/construct"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/client"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status/pvc"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/controllers/om/backup"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/create"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/watch"
	"github.com/10gen/ops-manager-kubernetes/pkg/agentVersionManagement"
	"github.com/10gen/ops-manager-kubernetes/pkg/images"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster/failedcluster"
	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster/memberwatch"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

var clusters = []string{"api1.kube.com", "api2.kube.com", "api3.kube.com"}

func checkMultiReconcileSuccessful(ctx context.Context, t *testing.T, reconciler reconcile.Reconciler, m *mdbmulti.MongoDBMultiCluster, client client.Client, shouldRequeue bool) {
	err := client.Update(ctx, m)
	assert.NoError(t, err)

	result, e := reconciler.Reconcile(ctx, requestFromObject(m))
	assert.NoError(t, e)
	if shouldRequeue {
		assert.True(t, result.Requeue || result.RequeueAfter > 0)
	} else {
		assert.Equal(t, reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}, result)
	}

	// fetch the last updates as the reconciliation loop can update the mdb resource.
	err = client.Get(ctx, kube.ObjectKey(m.Namespace, m.Name), m)
	assert.NoError(t, err)
}

func TestChangingFCVMultiCluster(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, cl, _, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, cl, false)

	// Helper function to update and verify FCV
	verifyFCV := func(version, expectedFCV string, fcvOverride *string, t *testing.T) {
		if fcvOverride != nil {
			mrs.Spec.FeatureCompatibilityVersion = fcvOverride
		}

		mrs.Spec.Version = version
		_ = cl.Update(ctx, mrs)
		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, cl, false)
		assert.Equal(t, expectedFCV, mrs.Status.FeatureCompatibilityVersion)
	}

	testFCVsCases(t, verifyFCV)
}

func TestCreateMultiReplicaSet(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()

	reconciler, client, _, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)
}

func TestMultiReplicaSetClusterReconcileContainerImages(t *testing.T) {
	databaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_1_0_0", util.NonStaticDatabaseEnterpriseImage)
	initDatabaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_2_0_0", util.InitDatabaseImageUrlEnv)

	imageUrlsMock := images.ImageUrls{
		databaseRelatedImageEnv:     "quay.io/mongodb/mongodb-enterprise-database:@sha256:MONGODB_DATABASE",
		initDatabaseRelatedImageEnv: "quay.io/mongodb/mongodb-enterprise-init-database:@sha256:MONGODB_INIT_DATABASE",
	}

	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).SetVersion("8.0.0").Build()
	reconciler, kubeClient, memberClients, _ := defaultMultiReplicaSetReconciler(ctx, imageUrlsMock, "2.0.0", "1.0.0", mrs)

	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, kubeClient, false)

	clusterSpecs, err := mrs.GetClusterSpecItems()
	require.NoError(t, err)
	for _, item := range clusterSpecs {
		c := memberClients[item.ClusterName]

		t.Run(item.ClusterName, func(t *testing.T) {
			sts := appsv1.StatefulSet{}
			err := c.Get(ctx, kube.ObjectKey(mrs.Namespace, mrs.MultiStatefulsetName(mrs.ClusterNum(item.ClusterName))), &sts)
			require.NoError(t, err)

			require.Len(t, sts.Spec.Template.Spec.InitContainers, 1)
			require.Len(t, sts.Spec.Template.Spec.Containers, 1)

			assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-init-database:@sha256:MONGODB_INIT_DATABASE", sts.Spec.Template.Spec.InitContainers[0].Image)
			assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-database:@sha256:MONGODB_DATABASE", sts.Spec.Template.Spec.Containers[0].Image)
		})
	}
}

func TestMultiReplicaSetClusterReconcileContainerImagesWithStaticArchitecture(t *testing.T) {
	t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))

	databaseRelatedImageEnv := fmt.Sprintf("RELATED_IMAGE_%s_8_0_0_ubi9", mcoConstruct.MongodbImageEnv)

	imageUrlsMock := images.ImageUrls{
		architectures.MdbAgentImageRepo: "quay.io/mongodb/mongodb-agent-ubi",
		mcoConstruct.MongodbImageEnv:    "quay.io/mongodb/mongodb-enterprise-server",
		databaseRelatedImageEnv:         "quay.io/mongodb/mongodb-enterprise-server:@sha256:MONGODB_DATABASE",
	}

	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).SetVersion("8.0.0").Build()
	reconciler, kubeClient, memberClients, omConnectionFactory := defaultMultiReplicaSetReconciler(ctx, imageUrlsMock, "", "", mrs)
	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		connection.(*om.MockedOmConnection).SetAgentVersion("12.0.30.7791-1", "")
	})

	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, kubeClient, false)

	clusterSpecs, err := mrs.GetClusterSpecItems()
	require.NoError(t, err)
	for _, item := range clusterSpecs {
		c := memberClients[item.ClusterName]

		t.Run(item.ClusterName, func(t *testing.T) {
			sts := appsv1.StatefulSet{}
			err := c.Get(ctx, kube.ObjectKey(mrs.Namespace, mrs.MultiStatefulsetName(mrs.ClusterNum(item.ClusterName))), &sts)
			require.NoError(t, err)

			assert.Len(t, sts.Spec.Template.Spec.InitContainers, 0)
			require.Len(t, sts.Spec.Template.Spec.Containers, 2)

			// Version from OM + operator version
			assert.Equal(t, "quay.io/mongodb/mongodb-agent-ubi:12.0.30.7791-1_9.9.9-test", sts.Spec.Template.Spec.Containers[0].Image)
			assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-server:@sha256:MONGODB_DATABASE", sts.Spec.Template.Spec.Containers[1].Image)
		})
	}
}

func TestReconcilePVCResizeMultiCluster(t *testing.T) {
	ctx := context.Background()

	configuration := v1.StatefulSetConfiguration{
		SpecWrapper: v1.StatefulSetSpecWrapper{
			Spec: appsv1.StatefulSetSpec{
				VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "data",
						},
						Spec: corev1.PersistentVolumeClaimSpec{
							StorageClassName: ptr.To("test"),
							Resources: corev1.VolumeResourceRequirements{
								Requests: map[corev1.ResourceName]resource.Quantity{corev1.ResourceStorage: resource.MustParse("1Gi")},
							},
						},
					},
				},
			},
		},
	}
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	mrs.Spec.StatefulSetConfiguration = &configuration

	reconciler, c, clusterMap, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)

	// first, we create the shardedCluster with sts and pvc,
	// no resize happening, even after running reconcile multiple times
	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, c, false)
	testNoResizeMulti(t, c, ctx, mrs)

	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, c, false)
	testNoResizeMulti(t, c, ctx, mrs)

	createdConfigPVCs := getPVCsMulti(t, ctx, mrs, clusterMap)

	newSize := "2Gi"
	configuration.SpecWrapper.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests = map[corev1.ResourceName]resource.Quantity{corev1.ResourceStorage: resource.MustParse(newSize)}
	mrs.Spec.StatefulSetConfiguration = &configuration
	err := c.Update(ctx, mrs)
	assert.NoError(t, err)

	_, e := reconciler.Reconcile(ctx, requestFromObject(mrs))
	assert.NoError(t, e)

	// its only one sts in the pvc status, since we haven't started the next one yet
	testMDBStatusMulti(t, c, ctx, mrs, status.PhasePending, status.PVCS{{Phase: pvc.PhasePVCResize, StatefulsetName: "temple-0"}})

	testPVCSizeHasIncreased(t, createdConfigPVCs[0].client, ctx, newSize, "temple-0")

	// Running the same resize makes no difference, we are still resizing
	_, e = reconciler.Reconcile(ctx, requestFromObject(mrs))
	assert.NoError(t, e)

	testMDBStatusMulti(t, c, ctx, mrs, status.PhasePending, status.PVCS{{Phase: pvc.PhasePVCResize, StatefulsetName: "temple-0"}})

	// update the first stses pvc to be ready
	for _, claim := range createdConfigPVCs[0].persistentVolumeClaims {
		setPVCWithUpdatedResource(ctx, t, createdConfigPVCs[0].client, &claim)
	}

	// Running reconcile again should go into orphan
	_, e = reconciler.Reconcile(ctx, requestFromObject(mrs))
	assert.NoError(t, e)

	// the second pvc is now getting resized
	testMDBStatusMulti(t, c, ctx, mrs, status.PhasePending, status.PVCS{
		{Phase: pvc.PhaseSTSOrphaned, StatefulsetName: "temple-0"},
		{Phase: pvc.PhasePVCResize, StatefulsetName: "temple-1"},
	})
	// Running reconcile again second pvcState should go into orphan, third one should start

	// update the first stse pvc to be ready
	for _, claim := range createdConfigPVCs[1].persistentVolumeClaims {
		setPVCWithUpdatedResource(ctx, t, createdConfigPVCs[1].client, &claim)
	}

	_, e = reconciler.Reconcile(ctx, requestFromObject(mrs))
	assert.NoError(t, e)

	testMDBStatusMulti(t, c, ctx, mrs, status.PhasePending, status.PVCS{
		{Phase: pvc.PhaseSTSOrphaned, StatefulsetName: "temple-0"},
		{Phase: pvc.PhaseSTSOrphaned, StatefulsetName: "temple-1"},
		{Phase: pvc.PhasePVCResize, StatefulsetName: "temple-2"},
	})

	// pvc aren't resized. therefore same status expected
	_, e = reconciler.Reconcile(ctx, requestFromObject(mrs))
	assert.NoError(t, e)

	testMDBStatusMulti(t, c, ctx, mrs, status.PhasePending, status.PVCS{
		{Phase: pvc.PhaseSTSOrphaned, StatefulsetName: "temple-0"},
		{Phase: pvc.PhaseSTSOrphaned, StatefulsetName: "temple-1"},
		{Phase: pvc.PhasePVCResize, StatefulsetName: "temple-2"},
	})

	// update the first stse pvc to be ready
	for _, claim := range createdConfigPVCs[2].persistentVolumeClaims {
		setPVCWithUpdatedResource(ctx, t, createdConfigPVCs[2].client, &claim)
	}

	// We move from resize → orphaned and in the final call in the reconciling to running and
	// remove the PVCs.
	_, err = reconciler.Reconcile(ctx, requestFromObject(mrs))
	assert.NoError(t, err)

	// We are now in the running phase, since all statefulsets have finished resizing; therefore,
	// no pvc phase is shown anymore
	testMDBStatusMulti(t, c, ctx, mrs, status.PhaseRunning, nil)

	for _, item := range mrs.Spec.ClusterSpecList {
		c := clusterMap[item.ClusterName]
		stsName := mrs.MultiStatefulsetName(mrs.ClusterNum(item.ClusterName))
		testStatefulsetHasAnnotationAndCorrectSize(t, c, ctx, mrs.Namespace, stsName)
	}

	_, e = reconciler.Reconcile(ctx, requestFromObject(mrs))
	require.NoError(t, e)

	// We are now in the running phase, since all statefulsets have finished resizing; therefore,
	// no pvc phase is shown anymore
	testMDBStatusMulti(t, c, ctx, mrs, status.PhaseRunning, nil)
}

type pvcClient struct {
	persistentVolumeClaims []corev1.PersistentVolumeClaim
	client                 client.Client
}

func getPVCsMulti(t *testing.T, ctx context.Context, mrs *mdbmulti.MongoDBMultiCluster, memberClusterMap map[string]client.Client) []pvcClient {
	var createdConfigPVCs []pvcClient
	for _, item := range mrs.Spec.ClusterSpecList {
		c := memberClusterMap[item.ClusterName]
		statefulSetName := mrs.MultiStatefulsetName(mrs.ClusterNum(item.ClusterName))
		sts := appsv1.StatefulSet{}
		err := c.Get(ctx, kube.ObjectKey(mrs.Namespace, statefulSetName), &sts)
		require.NoError(t, err)
		createdConfigPVCs = append(createdConfigPVCs, pvcClient{persistentVolumeClaims: createPVCs(t, sts, c), client: c})
	}
	return createdConfigPVCs
}

func testNoResizeMulti(t *testing.T, c kubernetesClient.Client, ctx context.Context, mrs *mdbmulti.MongoDBMultiCluster) {
	m := mdbmulti.MongoDBMultiCluster{}
	err := c.Get(ctx, kube.ObjectKey(mrs.Namespace, mrs.Name), &m)
	assert.NoError(t, err)
	assert.Nil(t, m.Status.PVCs)
}

func testMDBStatusMulti(t *testing.T, c kubernetesClient.Client, ctx context.Context, mrs *mdbmulti.MongoDBMultiCluster, expectedMDBPhase status.Phase, expectedPVCS status.PVCS) {
	m := mdbmulti.MongoDBMultiCluster{}
	err := c.Get(ctx, kube.ObjectKey(mrs.Namespace, mrs.Name), &m)
	require.NoError(t, err)
	require.Equal(t, expectedMDBPhase, m.Status.Phase)
	require.Equal(t, expectedPVCS, m.Status.PVCs)
}

func TestReconcileFails_WhenProjectConfig_IsNotFound(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()

	reconciler, _, _, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)

	result, err := reconciler.Reconcile(ctx, requestFromObject(mrs))
	assert.Nil(t, err)
	assert.True(t, result.RequeueAfter > 0)
}

func TestMultiClusterConfigMapAndSecretWatched(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()

	reconciler, client, _, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

	expected := map[watch.Object][]types.NamespacedName{
		{ResourceType: watch.ConfigMap, Resource: kube.ObjectKey(mock.TestNamespace, mock.TestProjectConfigMapName)}: {kube.ObjectKey(mock.TestNamespace, mrs.Name)},
		{ResourceType: watch.Secret, Resource: kube.ObjectKey(mock.TestNamespace, mrs.Spec.Credentials)}:             {kube.ObjectKey(mock.TestNamespace, mrs.Name)},
	}

	assert.Equal(t, reconciler.resourceWatcher.GetWatchedResources(), expected)
}

func TestServiceCreation_WithExternalName(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().
		SetClusterSpecList(clusters).
		SetExternalAccess(
			mdb.ExternalAccessConfiguration{
				ExternalDomain: ptr.To("cluster-%d.testing"),
			}, ptr.To("cluster-%d.testing")).
		Build()
	reconciler, client, memberClusterMap, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		assert.NoError(t, err)
	}
	clusterSpecs := clusterSpecList
	for _, item := range clusterSpecs {
		c := memberClusterMap[item.ClusterName]
		for podNum := 0; podNum < item.Members; podNum++ {
			externalService := getExternalService(mrs, item.ClusterName, podNum)

			err = c.Get(ctx, kube.ObjectKey(externalService.Namespace, externalService.Name), &corev1.Service{})
			assert.NoError(t, err)

			// ensure that all other clusters do not have this service
			for _, otherItem := range clusterSpecs {
				if item.ClusterName == otherItem.ClusterName {
					continue
				}
				otherCluster := memberClusterMap[otherItem.ClusterName]
				err = otherCluster.Get(ctx, kube.ObjectKey(externalService.Namespace, externalService.Name), &corev1.Service{})
				assert.Error(t, err)
			}
		}
	}
}

func TestServiceCreation_WithPlaceholders(t *testing.T) {
	ctx := context.Background()
	annotationsWithPlaceholders := map[string]string{
		create.PlaceholderPodIndex:            "{podIndex}",
		create.PlaceholderNamespace:           "{namespace}",
		create.PlaceholderResourceName:        "{resourceName}",
		create.PlaceholderPodName:             "{podName}",
		create.PlaceholderStatefulSetName:     "{statefulSetName}",
		create.PlaceholderExternalServiceName: "{externalServiceName}",
		create.PlaceholderMongodProcessDomain: "{mongodProcessDomain}",
		create.PlaceholderMongodProcessFQDN:   "{mongodProcessFQDN}",
		create.PlaceholderClusterName:         "{clusterName}",
		create.PlaceholderClusterIndex:        "{clusterIndex}",
	}
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().
		SetClusterSpecList(clusters).
		SetExternalAccess(
			mdb.ExternalAccessConfiguration{
				ExternalService: mdb.ExternalServiceConfiguration{
					Annotations: annotationsWithPlaceholders,
				},
			}, nil).
		Build()
	mrs.Spec.DuplicateServiceObjects = util.BooleanRef(false)
	reconciler, client, memberClusterMap, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		assert.NoError(t, err)
	}
	clusterSpecs := clusterSpecList
	for _, item := range clusterSpecs {
		c := memberClusterMap[item.ClusterName]
		for podNum := 0; podNum < item.Members; podNum++ {
			externalServiceName := fmt.Sprintf("%s-%d-%d-svc-external", mrs.Name, mrs.ClusterNum(item.ClusterName), podNum)

			svc := corev1.Service{}
			err = c.Get(ctx, kube.ObjectKey(mrs.Namespace, externalServiceName), &svc)
			assert.NoError(t, err)

			statefulSetName := fmt.Sprintf("%s-%d", mrs.Name, mrs.ClusterNum(item.ClusterName))
			podName := fmt.Sprintf("%s-%d", statefulSetName, podNum)
			mongodProcessDomain := fmt.Sprintf("%s.svc.cluster.local", mrs.Namespace)
			expectedAnnotations := map[string]string{
				create.PlaceholderPodIndex:            fmt.Sprintf("%d", podNum),
				create.PlaceholderNamespace:           mrs.Namespace,
				create.PlaceholderResourceName:        mrs.Name,
				create.PlaceholderStatefulSetName:     statefulSetName,
				create.PlaceholderPodName:             podName,
				create.PlaceholderExternalServiceName: fmt.Sprintf("%s-svc-external", podName),
				create.PlaceholderMongodProcessDomain: mongodProcessDomain,
				create.PlaceholderMongodProcessFQDN:   fmt.Sprintf("%s-svc.%s", podName, mongodProcessDomain),
				create.PlaceholderClusterName:         item.ClusterName,
				create.PlaceholderClusterIndex:        fmt.Sprintf("%d", mrs.ClusterNum(item.ClusterName)),
			}
			assert.Equal(t, expectedAnnotations, svc.Annotations)
		}
	}
}

func TestServiceCreation_WithoutDuplicates(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().
		SetClusterSpecList(clusters).
		Build()
	reconciler, client, memberClusterMap, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		assert.NoError(t, err)
	}
	clusterSpecs := clusterSpecList
	for _, item := range clusterSpecs {
		c := memberClusterMap[item.ClusterName]
		for podNum := 0; podNum < item.Members; podNum++ {
			svc := getService(mrs, item.ClusterName, podNum)

			testSvc := corev1.Service{}
			err := c.Get(ctx, kube.ObjectKey(svc.Namespace, svc.Name), &testSvc)
			assert.NoError(t, err)

			// ensure that all other clusters do not have this service
			for _, otherItem := range clusterSpecs {
				if item.ClusterName == otherItem.ClusterName {
					continue
				}
				otherCluster := memberClusterMap[otherItem.ClusterName]
				err = otherCluster.Get(ctx, kube.ObjectKey(svc.Namespace, svc.Name), &corev1.Service{})
				assert.Error(t, err)
			}
		}
	}
}

func TestServiceCreation_WithDuplicates(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().
		SetClusterSpecList(clusters).
		Build()
	mrs.Spec.DuplicateServiceObjects = util.BooleanRef(true)

	reconciler, client, memberClusterMap, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

	clusterSpecs, err := mrs.GetClusterSpecItems()
	if err != nil {
		assert.NoError(t, err)
	}
	for _, item := range clusterSpecs {
		for podNum := 0; podNum < item.Members; podNum++ {
			svc := getService(mrs, item.ClusterName, podNum)

			// ensure that all clusters have all services
			for _, otherItem := range clusterSpecs {
				otherCluster := memberClusterMap[otherItem.ClusterName]
				err := otherCluster.Get(ctx, kube.ObjectKey(svc.Namespace, svc.Name), &corev1.Service{})
				assert.NoError(t, err)
			}
		}
	}
}

func TestHeadlessServiceCreation(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().
		SetClusterSpecList(clusters).
		Build()

	reconciler, client, memberClusterMap, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

	clusterSpecs, err := mrs.GetClusterSpecItems()
	if err != nil {
		assert.NoError(t, err)
	}

	for _, item := range clusterSpecs {
		c := memberClusterMap[item.ClusterName]
		svcName := mrs.MultiHeadlessServiceName(mrs.ClusterNum(item.ClusterName))

		svc := &corev1.Service{}
		err := c.Get(ctx, kube.ObjectKey(mrs.Namespace, svcName), svc)
		assert.NoError(t, err)

		expectedMap := map[string]string{
			"app":                         mrs.MultiHeadlessServiceName(mrs.ClusterNum(item.ClusterName)),
			construct.ControllerLabelName: util.OperatorName,
		}
		assert.Equal(t, expectedMap, svc.Spec.Selector)
	}
}

func TestResourceDeletion(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, client, memberClients, omConnectionFactory := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

	t.Run("Resources are created", func(t *testing.T) {
		clusterSpecs, err := mrs.GetClusterSpecItems()
		if err != nil {
			assert.NoError(t, err)
		}
		for _, item := range clusterSpecs {
			c := memberClients[item.ClusterName]
			t.Run("Stateful Set in each member cluster has been created", func(t *testing.T) {
				ctx := context.Background()
				sts := appsv1.StatefulSet{}
				err := c.Get(ctx, kube.ObjectKey(mrs.Namespace, mrs.MultiStatefulsetName(mrs.ClusterNum(item.ClusterName))), &sts)
				assert.NoError(t, err)
			})

			t.Run("Services in each member cluster have been created", func(t *testing.T) {
				ctx := context.Background()
				svcList := corev1.ServiceList{}
				err := c.List(ctx, &svcList)
				assert.NoError(t, err)
				assert.Len(t, svcList.Items, item.Members+2)
			})

			t.Run("Configmaps in each member cluster have been created", func(t *testing.T) {
				ctx := context.Background()
				configMapList := corev1.ConfigMapList{}
				err := c.List(ctx, &configMapList)
				assert.NoError(t, err)
				assert.Len(t, configMapList.Items, 1)
			})
			t.Run("Secrets in each member cluster have been created", func(t *testing.T) {
				ctx := context.Background()
				secretList := corev1.SecretList{}
				err := c.List(ctx, &secretList)
				assert.NoError(t, err)
				assert.Len(t, secretList.Items, 1)
			})
		}
	})

	err := reconciler.deleteManagedResources(ctx, *mrs, zap.S())
	assert.NoError(t, err)

	clusterSpecs, err := mrs.GetClusterSpecItems()
	if err != nil {
		assert.NoError(t, err)
	}
	for _, item := range clusterSpecs {
		c := memberClients[item.ClusterName]
		t.Run("Stateful Set in each member cluster has been removed", func(t *testing.T) {
			ctx := context.Background()
			sts := appsv1.StatefulSet{}
			err := c.Get(ctx, kube.ObjectKey(mrs.Namespace, mrs.MultiStatefulsetName(mrs.ClusterNum(item.ClusterName))), &sts)
			assert.Error(t, err)
		})

		t.Run("Services in each member cluster have been removed", func(t *testing.T) {
			ctx := context.Background()
			svcList := corev1.ServiceList{}
			err := c.List(ctx, &svcList)
			assert.NoError(t, err)
			// temple-0-svc is leftover and not deleted since it does not contain the label: mongodbmulticluster -> my-namespace-temple
			assert.Len(t, svcList.Items, 1)
		})

		t.Run("Configmaps in each member cluster have been removed", func(t *testing.T) {
			ctx := context.Background()
			configMapList := corev1.ConfigMapList{}
			err := c.List(ctx, &configMapList)
			assert.NoError(t, err)
			assert.Len(t, configMapList.Items, 0)
		})

		t.Run("Secrets in each member cluster have been removed", func(t *testing.T) {
			ctx := context.Background()
			secretList := corev1.SecretList{}
			err := c.List(ctx, &secretList)
			assert.NoError(t, err)
			assert.Len(t, secretList.Items, 0)
		})
	}

	t.Run("Ops Manager state has been cleaned", func(t *testing.T) {
		processes := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses()
		assert.Len(t, processes, 0)

		ac, err := omConnectionFactory.GetConnection().ReadAutomationConfig()
		assert.NoError(t, err)

		assert.Empty(t, ac.Auth.AutoAuthMechanisms)
		assert.Empty(t, ac.Auth.DeploymentAuthMechanisms)
		assert.False(t, ac.Auth.IsEnabled())
	})
}

func TestGroupSecret_IsCopied_ToEveryMemberCluster(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, client, memberClusterMap, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

	for _, clusterName := range clusters {
		t.Run(fmt.Sprintf("Secret exists in cluster %s", clusterName), func(t *testing.T) {
			ctx := context.Background()
			c, ok := memberClusterMap[clusterName]
			assert.True(t, ok)

			s := corev1.Secret{}
			err := c.Get(ctx, kube.ObjectKey(mrs.Namespace, fmt.Sprintf("%s-group-secret", om.TestGroupID)), &s)
			assert.NoError(t, err)
			assert.Equal(t, mongoDBMultiLabels(mrs.Name, mrs.Namespace), s.Labels)
		})
	}
}

func TestAuthentication_IsEnabledInOM_WhenConfiguredInCR(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetSecurity(&mdb.Security{
		Authentication: &mdb.Authentication{Enabled: true, Modes: []mdb.AuthMode{"SCRAM"}},
	}).SetClusterSpecList(clusters).Build()

	reconciler, client, _, omConnectionFactory := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)

	t.Run("Reconciliation is successful when configuring scram", func(t *testing.T) {
		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)
	})

	t.Run("Automation Config has been updated correctly", func(t *testing.T) {
		ac, err := omConnectionFactory.GetConnection().ReadAutomationConfig()
		assert.NoError(t, err)

		assert.Contains(t, ac.Auth.AutoAuthMechanism, "SCRAM-SHA-256")
		assert.Contains(t, ac.Auth.DeploymentAuthMechanisms, "SCRAM-SHA-256")
		assert.True(t, ac.Auth.IsEnabled())
		assert.NotEmpty(t, ac.Auth.AutoPwd)
		assert.NotEmpty(t, ac.Auth.Key)
		assert.NotEmpty(t, ac.Auth.KeyFile)
		assert.NotEmpty(t, ac.Auth.KeyFileWindows)
		assert.NotEmpty(t, ac.Auth.AutoUser)
	})
}

func TestTls_IsEnabledInOM_WhenConfiguredInCR(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).SetSecurity(&mdb.Security{
		TLSConfig:                 &mdb.TLSConfig{Enabled: true, CA: "some-ca"},
		CertificatesSecretsPrefix: "some-prefix",
	}).Build()

	reconciler, client, _, omConnectionFactory := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
	createMultiClusterReplicaSetTLSData(t, ctx, client, mrs, "some-ca")

	t.Run("Reconciliation is successful when configuring tls", func(t *testing.T) {
		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)
	})

	t.Run("Automation Config has been updated correctly", func(t *testing.T) {
		processes := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses()
		for _, p := range processes {
			assert.True(t, p.IsTLSEnabled())
			assert.Equal(t, "requireTLS", p.TLSConfig()["mode"])
		}
	})
}

func TestSpecIsSavedAsAnnotation_WhenReconciliationIsSuccessful(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, client, _, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

	// fetch the resource after reconciliation
	err := client.Get(ctx, kube.ObjectKey(mrs.Namespace, mrs.Name), mrs)
	assert.NoError(t, err)

	expected := mrs.Spec
	actual, err := mrs.ReadLastAchievedSpec()
	assert.NoError(t, err)
	assert.NotNil(t, actual)

	areEqual, err := specsAreEqual(expected, *actual)

	assert.NoError(t, err)
	assert.True(t, areEqual)
}

func TestMultiReplicaSetRace(t *testing.T) {
	ctx := context.Background()
	rs1, cfgMap1, projectName1 := buildMultiReplicaSetWithCustomProject("my-rs1")
	rs2, cfgMap2, projectName2 := buildMultiReplicaSetWithCustomProject("my-rs2")
	rs3, cfgMap3, projectName3 := buildMultiReplicaSetWithCustomProject("my-rs3")

	resourceToProjectMapping := map[string]string{
		"my-rs1": projectName1,
		"my-rs2": projectName2,
		"my-rs3": projectName3,
	}

	fakeClient := mock.NewEmptyFakeClientBuilder().
		WithObjects(rs1, rs2, rs3).
		WithObjects(cfgMap1, cfgMap2, cfgMap3).
		WithObjects(mock.GetCredentialsSecret(om.TestUser, om.TestApiKey)).
		Build()

	omConnectionFactory := om.NewDefaultCachedOMConnectionFactory().WithResourceToProjectMapping(resourceToProjectMapping)
	memberClusterMap := getFakeMultiClusterMapWithConfiguredInterceptor(clusters, omConnectionFactory, true, true)
	reconciler := newMultiClusterReplicaSetReconciler(ctx, fakeClient, nil, "fake-initDatabaseNonStaticImageVersion", "fake-databaseNonStaticImageVersion", false, omConnectionFactory.GetConnectionFunc, memberClusterMap)

	testConcurrentReconciles(ctx, t, fakeClient, reconciler, rs1, rs2, rs3)
}

func buildMultiReplicaSetWithCustomProject(mcReplicaSetName string) (*mdbmulti.MongoDBMultiCluster, *corev1.ConfigMap, string) {
	configMapName := mock.TestProjectConfigMapName + "-" + mcReplicaSetName
	projectName := om.TestGroupName + "-" + mcReplicaSetName

	return mdbmulti.DefaultMultiReplicaSetBuilder().
		SetName(mcReplicaSetName).
		SetOpsManagerConfigMapName(configMapName).
		SetClusterSpecList(clusters).
		Build(), mock.GetProjectConfigMap(configMapName, projectName, ""), projectName
}

func TestScaling(t *testing.T) {
	ctx := context.Background()

	t.Run("Can scale to max amount when creating the resource", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		reconciler, client, memberClusters, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

		statefulSets := readStatefulSets(ctx, mrs, memberClusters)
		assert.Len(t, statefulSets, 3)

		clusterSpecs, err := mrs.GetClusterSpecItems()
		if err != nil {
			assert.NoError(t, err)
		}
		for _, item := range clusterSpecs {
			sts := statefulSets[item.ClusterName]
			assert.Equal(t, item.Members, int(*sts.Spec.Replicas))
		}
	})

	t.Run("Scale one at a time when scaling up", func(t *testing.T) {
		stsWrapper := &v1.StatefulSetConfiguration{
			SpecWrapper: v1.StatefulSetSpecWrapper{
				Spec: appsv1.StatefulSetSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"a": "b"},
					},
				},
			},
		}
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		mrs.Spec.ClusterSpecList[0].Members = 1
		mrs.Spec.ClusterSpecList[0].StatefulSetConfiguration = stsWrapper
		mrs.Spec.ClusterSpecList[1].Members = 1
		mrs.Spec.ClusterSpecList[2].Members = 1
		reconciler, client, memberClusters, omConnectionFactory := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)
		statefulSets := readStatefulSets(ctx, mrs, memberClusters)
		clusterSpecs, err := mrs.GetClusterSpecItems()
		if err != nil {
			assert.NoError(t, err)
		}
		for _, item := range clusterSpecs {
			sts := statefulSets[item.ClusterName]
			assert.Equal(t, 1, int(*sts.Spec.Replicas))
		}

		// make sure we return internal object modifications
		assert.Equal(t, clusterSpecs[0].StatefulSetConfiguration, stsWrapper)

		// scale up in two different clusters at once.
		mrs.Spec.ClusterSpecList[0].Members = 3
		mrs.Spec.ClusterSpecList[2].Members = 3

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 2, 1, 1)
		assert.Len(t, omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses(), 4)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 3, 1, 1)
		assert.Len(t, omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses(), 5)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 3, 1, 2)
		assert.Len(t, omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses(), 6)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 3, 1, 3)
		assert.Len(t, omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses(), 7)

		clusterSpecs, _ = mrs.GetClusterSpecItems()
		// make sure we return internal object modifications
		assert.Equal(t, clusterSpecs[0].StatefulSetConfiguration, stsWrapper)
	})

	t.Run("Scale one at a time when scaling down", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		mrs.Spec.ClusterSpecList[0].Members = 3
		mrs.Spec.ClusterSpecList[1].Members = 2
		mrs.Spec.ClusterSpecList[2].Members = 3
		reconciler, client, memberClusters, omConnectionFactory := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)
		statefulSets := readStatefulSets(ctx, mrs, memberClusters)
		clusterSpecList, err := mrs.GetClusterSpecItems()
		if err != nil {
			assert.NoError(t, err)
		}

		for _, item := range clusterSpecList {
			sts := statefulSets[item.ClusterName]
			assert.Equal(t, item.Members, int(*sts.Spec.Replicas))
		}

		mockedOmConnection := omConnectionFactory.GetConnection().(*om.MockedOmConnection)
		assert.Len(t, mockedOmConnection.GetProcesses(), 8)

		// scale down in all clusters.
		mrs.Spec.ClusterSpecList[0].Members = 1
		mrs.Spec.ClusterSpecList[1].Members = 1
		mrs.Spec.ClusterSpecList[2].Members = 1

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 2, 2, 3)
		assert.Len(t, mockedOmConnection.GetProcesses(), 7)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 1, 2, 3)
		assert.Len(t, mockedOmConnection.GetProcesses(), 6)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 1, 1, 3)
		assert.Len(t, mockedOmConnection.GetProcesses(), 5)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 1, 1, 2)
		assert.Len(t, mockedOmConnection.GetProcesses(), 4)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 1, 1, 1)
		assert.Len(t, mockedOmConnection.GetProcesses(), 3)
	})

	t.Run("Added members don't have overlapping replica set member Ids", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		mrs.Spec.ClusterSpecList[0].Members = 1
		mrs.Spec.ClusterSpecList[1].Members = 1
		mrs.Spec.ClusterSpecList[2].Members = 1
		reconciler, client, _, omConnectionFactory := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

		assert.Len(t, omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses(), 3)

		dep, err := omConnectionFactory.GetConnection().ReadDeployment()
		assert.NoError(t, err)

		replicaSets := dep.ReplicaSets()

		assert.Len(t, replicaSets, 1)
		members := replicaSets[0].Members()
		assert.Len(t, members, 3)

		assertMemberNameAndId(t, members, fmt.Sprintf("%s-0-0", mrs.Name), 0)
		assertMemberNameAndId(t, members, fmt.Sprintf("%s-1-0", mrs.Name), 1)
		assertMemberNameAndId(t, members, fmt.Sprintf("%s-2-0", mrs.Name), 2)

		assert.Equal(t, members[0].Id(), 0)
		assert.Equal(t, members[1].Id(), 1)
		assert.Equal(t, members[2].Id(), 2)

		mrs.Spec.ClusterSpecList[0].Members = 2

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

		dep, err = omConnectionFactory.GetConnection().ReadDeployment()
		assert.NoError(t, err)

		replicaSets = dep.ReplicaSets()

		assert.Len(t, replicaSets, 1)
		members = replicaSets[0].Members()
		assert.Len(t, members, 4)

		assertMemberNameAndId(t, members, fmt.Sprintf("%s-0-0", mrs.Name), 0)
		assertMemberNameAndId(t, members, fmt.Sprintf("%s-0-1", mrs.Name), 3)
		assertMemberNameAndId(t, members, fmt.Sprintf("%s-1-0", mrs.Name), 1)
		assertMemberNameAndId(t, members, fmt.Sprintf("%s-2-0", mrs.Name), 2)
	})

	t.Run("Cluster can be added", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		mrs.Spec.ClusterSpecList = mrs.Spec.ClusterSpecList[:len(mrs.Spec.ClusterSpecList)-1]

		mrs.Spec.ClusterSpecList[0].Members = 1
		mrs.Spec.ClusterSpecList[1].Members = 1

		reconciler, client, memberClusters, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 1, 1)

		// scale one member and add a new cluster
		mrs.Spec.ClusterSpecList[0].Members = 3
		mrs.Spec.ClusterSpecList = append(mrs.Spec.ClusterSpecList, mdb.ClusterSpecItem{
			ClusterName: clusters[2],
			Members:     3,
		})

		err := client.Update(ctx, mrs)
		assert.NoError(t, err)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 2, 1, 0)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 3, 1, 0)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 3, 1, 1)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 3, 1, 2)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 3, 1, 3)
	})

	t.Run("Cluster can be removed", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()

		mrs.Spec.ClusterSpecList[0].Members = 3
		mrs.Spec.ClusterSpecList[1].Members = 2
		mrs.Spec.ClusterSpecList[2].Members = 3

		reconciler, client, memberClusters, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 3, 2, 3)

		mrs.Spec.ClusterSpecList[0].Members = 1
		mrs.Spec.ClusterSpecList = mrs.Spec.ClusterSpecList[:len(mrs.Spec.ClusterSpecList)-1]

		err := client.Update(ctx, mrs)
		assert.NoError(t, err)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 2, 2, 3)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 1, 2, 3)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 1, 2, 2)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 1, 2, 1)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 1, 2)

		// can reconcile again and it succeeds.
		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 1, 2)
	})

	t.Run("Multiple clusters can be removed", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()

		mrs.Spec.ClusterSpecList[0].Members = 2
		mrs.Spec.ClusterSpecList[1].Members = 1
		mrs.Spec.ClusterSpecList[2].Members = 2

		reconciler, client, memberClusters, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 2, 1, 2)

		// remove first and last
		mrs.Spec.ClusterSpecList = mdb.ClusterSpecList{mrs.Spec.ClusterSpecList[1]}

		err := client.Update(ctx, mrs)
		assert.NoError(t, err)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 1, 1, 2)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 0, 1, 2)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, true)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 0, 1, 1)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)
		assertStatefulSetReplicas(ctx, t, mrs, memberClusters, 0, 1, 0)
	})
}

func TestClusterNumbering(t *testing.T) {
	ctx := context.Background()

	t.Run("Create MDB CR first time", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		reconciler, client, _, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

		clusterNumMap := getClusterNumMapping(t, mrs)
		assertClusterpresent(t, clusterNumMap, mrs.Spec.ClusterSpecList, []int{0, 1, 2})
	})

	t.Run("Add Cluster", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
		mrs.Spec.ClusterSpecList = mrs.Spec.ClusterSpecList[:len(mrs.Spec.ClusterSpecList)-1]

		reconciler, client, _, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

		clusterNumMap := getClusterNumMapping(t, mrs)
		assertClusterpresent(t, clusterNumMap, mrs.Spec.ClusterSpecList, []int{0, 1})

		// add cluster
		mrs.Spec.ClusterSpecList = append(mrs.Spec.ClusterSpecList, mdb.ClusterSpecItem{
			ClusterName: clusters[2],
			Members:     1,
		})

		err := client.Update(ctx, mrs)
		assert.NoError(t, err)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)
		clusterNumMap = getClusterNumMapping(t, mrs)

		assert.Equal(t, 2, clusterNumMap[clusters[2]])
	})

	t.Run("Remove and Add back cluster", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()

		mrs.Spec.ClusterSpecList[0].Members = 1
		mrs.Spec.ClusterSpecList[1].Members = 1
		mrs.Spec.ClusterSpecList[2].Members = 1

		reconciler, client, _, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

		clusterNumMap := getClusterNumMapping(t, mrs)
		assertClusterpresent(t, clusterNumMap, mrs.Spec.ClusterSpecList, []int{0, 1, 2})
		clusterOneIndex := clusterNumMap[clusters[1]]

		// Remove cluster index 1 from the specs
		mrs.Spec.ClusterSpecList = mdb.ClusterSpecList{
			{
				ClusterName: clusters[0],
				Members:     1,
			},
			{
				ClusterName: clusters[2],
				Members:     1,
			},
		}
		err := client.Update(ctx, mrs)
		assert.NoError(t, err)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

		// Add cluster index 1 back to the specs
		mrs.Spec.ClusterSpecList = append(mrs.Spec.ClusterSpecList, mdb.ClusterSpecItem{
			ClusterName: clusters[1],
			Members:     1,
		})

		err = client.Update(ctx, mrs)
		assert.NoError(t, err)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)
		// assert the index corresponsing to cluster 1 is still 1
		clusterNumMap = getClusterNumMapping(t, mrs)
		assert.Equal(t, clusterOneIndex, clusterNumMap[clusters[1]])
	})
}

func getClusterNumMapping(t *testing.T, m *mdbmulti.MongoDBMultiCluster) map[string]int {
	clusterMapping := make(map[string]int)
	bytes := m.Annotations[mdbmulti.LastClusterNumMapping]
	err := json.Unmarshal([]byte(bytes), &clusterMapping)
	assert.NoError(t, err)

	return clusterMapping
}

// assertMemberNameAndId makes sure that the member with the given name has the given id.
// the processes are sorted and the order in the automation config is not necessarily the order
// in which they appear in the CR.
func assertMemberNameAndId(t *testing.T, members []om.ReplicaSetMember, name string, id int) {
	for _, m := range members {
		if m.Name() == name {
			assert.Equal(t, id, m.Id())
			return
		}
	}
	t.Fatalf("Member with name %s not found in replica set members", name)
}

func TestBackupConfigurationReplicaSet(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).
		SetConnectionSpec(testConnectionSpec()).
		SetBackup(mdb.Backup{
			Mode: "enabled",
		}).Build()

	reconciler, client, _, omConnectionFactory := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
	uuidStr := uuid.New().String()
	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		_, err := connection.UpdateBackupConfig(&backup.Config{
			ClusterId: uuidStr,
			Status:    backup.Inactive,
		})
		if err != nil {
			panic(err)
		}

		// add the Replicaset cluster to OM
		connection.(*om.MockedOmConnection).BackupHostClusters[uuidStr] = &backup.HostCluster{
			ReplicaSetName: mrs.Name,
			ClusterName:    mrs.Name,
			TypeName:       "REPLICA_SET",
		}
	})

	t.Run("Backup can be started", func(t *testing.T) {
		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)
		configResponse, _ := omConnectionFactory.GetConnection().ReadBackupConfigs()

		assert.Len(t, configResponse.Configs, 1)
		config := configResponse.Configs[0]

		assert.Equal(t, backup.Started, config.Status)
		assert.Equal(t, uuidStr, config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
	})

	t.Run("Backup snapshot schedule tests", backupSnapshotScheduleTests(mrs, client, reconciler, omConnectionFactory, uuidStr))

	t.Run("Backup can be stopped", func(t *testing.T) {
		mrs.Spec.Backup.Mode = "disabled"
		err := client.Update(ctx, mrs)
		assert.NoError(t, err)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

		configResponse, _ := omConnectionFactory.GetConnection().ReadBackupConfigs()
		assert.Len(t, configResponse.Configs, 1)

		config := configResponse.Configs[0]

		assert.Equal(t, backup.Stopped, config.Status)
		assert.Equal(t, uuidStr, config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
	})

	t.Run("Backup can be terminated", func(t *testing.T) {
		mrs.Spec.Backup.Mode = "terminated"
		err := client.Update(ctx, mrs)
		assert.NoError(t, err)

		checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

		configResponse, _ := omConnectionFactory.GetConnection().ReadBackupConfigs()
		assert.Len(t, configResponse.Configs, 1)

		config := configResponse.Configs[0]

		assert.Equal(t, backup.Terminating, config.Status)
		assert.Equal(t, uuidStr, config.ClusterId)
		assert.Equal(t, "PRIMARY", config.SyncSource)
	})
}

func TestMultiClusterFailover(t *testing.T) {
	ctx := context.Background()
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()

	reconciler, client, memberClusters, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)
	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

	// trigger failover by adding an annotation to the CR
	// read the first cluster from the clusterSpec list and fail it over.
	expectedNodeCount := 0
	for _, e := range mrs.Spec.ClusterSpecList {
		expectedNodeCount += e.Members
	}

	cluster := mrs.Spec.ClusterSpecList[0]
	failedClusters := []failedcluster.FailedCluster{{ClusterName: cluster.ClusterName, Members: cluster.Members}}

	clusterSpecBytes, err := json.Marshal(failedClusters)
	assert.NoError(t, err)

	mrs.SetAnnotations(map[string]string{failedcluster.FailedClusterAnnotation: string(clusterSpecBytes)})

	err = client.Update(ctx, mrs)
	assert.NoError(t, err)

	os.Setenv("PERFORM_FAILOVER", "true")
	defer os.Unsetenv("PERFORM_FAILOVER")

	err = memberwatch.AddFailoverAnnotation(ctx, *mrs, cluster.ClusterName, client)
	assert.NoError(t, err)
	require.NoError(t, client.Get(ctx, kube.ObjectKeyFromApiObject(mrs), mrs))

	checkMultiReconcileSuccessful(ctx, t, reconciler, mrs, client, false)

	// assert the statefulset member count in the healthy cluster is same as the initial count
	statefulSets := readStatefulSets(ctx, mrs, memberClusters)
	currentNodeCount := 0

	// only 2 clusters' statefulsets should be fetched since the first cluster has been failed-over
	assert.Equal(t, 2, len(statefulSets))

	for _, s := range statefulSets {
		currentNodeCount += int(*s.Spec.Replicas)
	}

	assert.Equal(t, expectedNodeCount, currentNodeCount)
}

func TestMultiReplicaSet_AgentVersionMapping(t *testing.T) {
	ctx := context.Background()
	defaultResource := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	containers := []corev1.Container{{Name: util.AgentContainerName, Image: "foo"}}
	podTemplate := corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: containers,
		},
	}
	overridenResource := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).SetPodSpecTemplate(podTemplate).Build()
	nonExistingPath := "/foo/bar/foo"

	t.Run("Static architecture, version retrieving fails, image is overriden, reconciliation should succeeds", func(t *testing.T) {
		t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))
		t.Setenv(agentVersionManagement.MappingFilePathEnv, nonExistingPath)
		overridenReconciler, overridenClient, _, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", overridenResource)
		checkMultiReconcileSuccessful(ctx, t, overridenReconciler, overridenResource, overridenClient, false)
	})

	t.Run("Static architecture, version retrieving fails, image is not overriden, reconciliation should fail", func(t *testing.T) {
		t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))
		t.Setenv(agentVersionManagement.MappingFilePathEnv, nonExistingPath)
		reconciler, client, _, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", defaultResource)
		checkMultiReconcileSuccessful(ctx, t, reconciler, defaultResource, client, true)
	})

	t.Run("Static architecture, version retrieving succeeds, image is not overriden, reconciliation should succeed", func(t *testing.T) {
		t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))
		reconciler, client, _, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", defaultResource)
		checkMultiReconcileSuccessful(ctx, t, reconciler, defaultResource, client, false)
	})

	t.Run("Non-Static architecture, version retrieving fails, image is not overriden, reconciliation should succeed", func(t *testing.T) {
		t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.NonStatic))
		t.Setenv(agentVersionManagement.MappingFilePathEnv, nonExistingPath)
		reconciler, client, _, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", defaultResource)
		checkMultiReconcileSuccessful(ctx, t, reconciler, defaultResource, client, false)
	})
}

func TestValidationsRunOnReconcile(t *testing.T) {
	ctx := context.Background()
	duplicateName := "duplicate"
	clustersWithDuplicate := []string{duplicateName, duplicateName, "cluster-3"}
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clustersWithDuplicate).Build()
	reconciler, client, _, _ := defaultMultiReplicaSetReconciler(ctx, nil, "", "", mrs)

	// copied
	err := client.Update(ctx, mrs)
	assert.NoError(t, err)

	_, err = reconciler.Reconcile(ctx, requestFromObject(mrs))
	assert.NoError(t, err)

	// fetch the last updates as the reconciliation loop can update the mdb resource.
	err = client.Get(ctx, kube.ObjectKey(mrs.Namespace, mrs.Name), mrs)
	assert.NoError(t, err)
	assert.Equal(t, status.PhaseFailed, mrs.Status.Phase)
	assert.Equal(t, fmt.Sprintf("Multiple clusters with the same name (%s) are not allowed", duplicateName), mrs.Status.Message)
}

func assertClusterpresent(t *testing.T, m map[string]int, specs mdb.ClusterSpecList, arr []int) {
	tmp := make([]int, 0)
	for _, s := range specs {
		tmp = append(tmp, m[s.ClusterName])
	}

	sort.Ints(tmp)
	assert.Equal(t, arr, tmp)
}

func assertStatefulSetReplicas(ctx context.Context, t *testing.T, mrs *mdbmulti.MongoDBMultiCluster, memberClusters map[string]client.Client, expectedReplicas ...int) {
	statefulSets := readStatefulSets(ctx, mrs, memberClusters)

	for i := range expectedReplicas {
		if val, ok := statefulSets[clusters[i]]; ok {
			require.Equal(t, expectedReplicas[i], int(*val.Spec.Replicas))
		}
	}
}

func readStatefulSets(ctx context.Context, mrs *mdbmulti.MongoDBMultiCluster, memberClusters map[string]client.Client) map[string]appsv1.StatefulSet {
	allStatefulSets := map[string]appsv1.StatefulSet{}
	clusterSpecList, err := mrs.GetClusterSpecItems()
	if err != nil {
		panic(err)
	}

	for _, item := range clusterSpecList {
		memberClient := memberClusters[item.ClusterName]
		sts := appsv1.StatefulSet{}
		err := memberClient.Get(ctx, kube.ObjectKey(mrs.Namespace, mrs.MultiStatefulsetName(mrs.ClusterNum(item.ClusterName))), &sts)
		if err == nil {
			allStatefulSets[item.ClusterName] = sts
		}
	}
	return allStatefulSets
}

// specsAreEqual compares two different MongoDBMultiSpec instances and returns true if they are equal.
// the specs need to be marshaled and bytes compared as this ensures that empty slices are converted to nil
// ones and gives an accurate comparison.
// We are unable to use reflect.DeepEqual for this comparision as when deserialization happens,
// some fields on spec2 are nil, while spec1 are empty collections. By converting both to bytes
// we can ensure they are equivalent for our purposes.
func specsAreEqual(spec1, spec2 mdbmulti.MongoDBMultiSpec) (bool, error) {
	spec1Bytes, err := json.Marshal(spec1)
	if err != nil {
		return false, err
	}
	spec2Bytes, err := json.Marshal(spec2)
	if err != nil {
		return false, err
	}
	return bytes.Equal(spec1Bytes, spec2Bytes), nil
}

func defaultMultiReplicaSetReconciler(ctx context.Context, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, m *mdbmulti.MongoDBMultiCluster) (*ReconcileMongoDbMultiReplicaSet, kubernetesClient.Client, map[string]client.Client, *om.CachedOMConnectionFactory) {
	multiReplicaSetController, client, clusterMap, omConnectionFactory := multiReplicaSetReconciler(ctx, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, m)
	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		connection.(*om.MockedOmConnection).Hostnames = calculateHostNamesForExternalDomains(m)
	})

	return multiReplicaSetController, client, clusterMap, omConnectionFactory
}

func calculateHostNamesForExternalDomains(m *mdbmulti.MongoDBMultiCluster) []string {
	if m.Spec.GetExternalDomain() == nil {
		return nil
	}

	var expectedHostnames []string
	for i, cl := range m.Spec.ClusterSpecList {
		for j := 0; j < cl.Members; j++ {
			externalDomain := m.Spec.GetExternalDomainForMemberCluster(cl.ClusterName)
			if externalDomain == nil {
				// we don't have all externalDomains set, so we don't calculate them here at all
				// validation should capture invalid external domains configuration, so it must be all or nothing
				return nil
			}
			expectedHostnames = append(expectedHostnames, fmt.Sprintf("%s-%d-%d.%s", m.Name, i, j, *externalDomain))
		}
	}
	return expectedHostnames
}

func multiReplicaSetReconciler(ctx context.Context, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, m *mdbmulti.MongoDBMultiCluster) (*ReconcileMongoDbMultiReplicaSet, kubernetesClient.Client, map[string]client.Client, *om.CachedOMConnectionFactory) {
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(m)
	memberClusterMap := getFakeMultiClusterMap(omConnectionFactory)
	return newMultiClusterReplicaSetReconciler(ctx, kubeClient, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, false, omConnectionFactory.GetConnectionFunc, memberClusterMap), kubeClient, memberClusterMap, omConnectionFactory
}

func getFakeMultiClusterMap(omConnectionFactory *om.CachedOMConnectionFactory) map[string]client.Client {
	return getFakeMultiClusterMapWithClusters(clusters, omConnectionFactory)
}

func getFakeMultiClusterMapWithClusters(clusters []string, omConnectionFactory *om.CachedOMConnectionFactory) map[string]client.Client {
	return getFakeMultiClusterMapWithConfiguredInterceptor(clusters, omConnectionFactory, true, true)
}

func getFakeMultiClusterMapWithConfiguredInterceptor(clusters []string, omConnectionFactory *om.CachedOMConnectionFactory, markStsAsReady bool, addOMHosts bool) map[string]client.Client {
	clientMap := make(map[string]client.Client)

	for _, e := range clusters {
		fakeClientBuilder := mock.NewEmptyFakeClientBuilder()
		fakeClientBuilder.WithInterceptorFuncs(interceptor.Funcs{
			Get: mock.GetFakeClientInterceptorGetFunc(omConnectionFactory, markStsAsReady, addOMHosts),
		})

		clientMap[e] = kubernetesClient.NewClient(fakeClientBuilder.Build())
	}
	return clientMap
}

func getFakeMultiClusterMapWithoutInterceptor(clusters []string) map[string]client.Client {
	clientMap := make(map[string]client.Client)

	for _, e := range clusters {
		memberCluster := multicluster.New(kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build()))
		clientMap[e] = memberCluster.GetClient()
	}
	return clientMap
}
