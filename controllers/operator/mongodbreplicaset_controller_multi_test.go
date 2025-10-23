package operator

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

var multiClusters = []string{"api1.kube.com", "api2.kube.com", "api3.kube.com"}

func TestCreateMultiClusterReplicaSet(t *testing.T) {
	ctx := context.Background()

	rs := mdbv1.NewDefaultMultiReplicaSetBuilder().
		SetClusterSpectList(multiClusters).
		Build()

	reconciler, kubeClient, memberClients, omConnectionFactory := defaultMultiClusterReplicaSetReconciler(ctx, nil, "", "", rs)
	checkMultiReplicaSetReconcileSuccessful(ctx, t, reconciler, rs, kubeClient, false)

	// Verify StatefulSets exist in each member cluster
	for i, clusterName := range multiClusters {
		memberClient := memberClients[clusterName]
		sts := appsv1.StatefulSet{}
		stsName := fmt.Sprintf("%s-%d", rs.Name, i)
		err := memberClient.Get(ctx, kube.ObjectKey(rs.Namespace, stsName), &sts)
		require.NoError(t, err, "StatefulSet should exist in cluster %s", clusterName)
		assert.Equal(t, int32(1), *sts.Spec.Replicas, "Replicas in %s", clusterName)
	}

	// Verify OM automation config has all processes
	processes := omConnectionFactory.GetConnection().(*om.MockedOmConnection).GetProcesses()
	assert.Len(t, processes, 3)
}

// Helper functions below

func checkMultiReplicaSetReconcileSuccessful(
	ctx context.Context,
	t *testing.T,
	reconciler reconcile.Reconciler,
	m *mdbv1.MongoDB,
	client client.Client,
	shouldRequeue bool,
) {
	err := client.Update(ctx, m)
	assert.NoError(t, err)

	result, e := reconciler.Reconcile(ctx, requestFromObject(m))
	assert.NoError(t, e)

	if shouldRequeue {
		assert.True(t, result.Requeue || result.RequeueAfter > 0)
	} else {
		assert.Equal(t, reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}, result)
	}

	err = client.Get(ctx, kube.ObjectKey(m.Namespace, m.Name), m)
	assert.NoError(t, err)
}

func multiClusterReplicaSetReconciler(
	ctx context.Context,
	imageUrls images.ImageUrls,
	initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string,
	rs *mdbv1.MongoDB,
) (*ReconcileMongoDbReplicaSet, kubernetesClient.Client, map[string]client.Client, *om.CachedOMConnectionFactory) {
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(rs)
	memberClusterMap := getMockMultiClusterMap(omConnectionFactory)

	return newReplicaSetReconciler(
		ctx,
		kubeClient,
		imageUrls,
		initDatabaseNonStaticImageVersion,
		databaseNonStaticImageVersion,
		false,
		false,
		memberClusterMap,
		omConnectionFactory.GetConnectionFunc,
	), kubeClient, memberClusterMap, omConnectionFactory
}

func defaultMultiClusterReplicaSetReconciler(
	ctx context.Context,
	imageUrls images.ImageUrls,
	initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string,
	rs *mdbv1.MongoDB,
) (*ReconcileMongoDbReplicaSet, kubernetesClient.Client, map[string]client.Client, *om.CachedOMConnectionFactory) {
	multiReplicaSetController, client, clusterMap, omConnectionFactory := multiClusterReplicaSetReconciler(
		ctx, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, rs,
	)

	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		connection.(*om.MockedOmConnection).Hostnames = nil
	})

	return multiReplicaSetController, client, clusterMap, omConnectionFactory
}

// getMockMultiClusterMap simulates multiple K8s clusters using fake clients
func getMockMultiClusterMap(omConnectionFactory *om.CachedOMConnectionFactory) map[string]client.Client {
	clientMap := make(map[string]client.Client)

	for _, clusterName := range multiClusters {
		fakeClientBuilder := mock.NewEmptyFakeClientBuilder()
		fakeClientBuilder.WithInterceptorFuncs(interceptor.Funcs{
			Get: mock.GetFakeClientInterceptorGetFunc(omConnectionFactory, true, true),
		})

		clientMap[clusterName] = kubernetesClient.NewClient(fakeClientBuilder.Build())
	}

	return clientMap
}
