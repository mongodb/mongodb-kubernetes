package operator

import (
	"context"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"testing"
)

// NedDefaultMultiReplicaSetBuilder

func TestCreateMultiClusterReplicaSet(t *testing.T) {
	ctx := context.Background()
	rs := mdbv1.NewDefaultMultiReplicaSetBuilder().Build()

	reconciler, client, _, _ := defaultMultiClusterReplicaSetReconciler(ctx, nil, "", "", mrs)
	checkMultiReplicaSetReconcileSuccessful(ctx, t, reconciler, rs, client, false)
}

func checkMultiReplicaSetReconcileSuccessful(ctx context.Context, t *testing.T, reconciler reconcile.Reconciler, m *mdbv1.MongoDB, client client.Client, shouldRequeue bool) {
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

func multiClusterReplicaSetReconciler(ctx context.Context, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, m *mdbv1.MongoDB) (*ReconcileMongoDbReplicaSet, kubernetesClient.Client, map[string]client.Client, *om.CachedOMConnectionFactory) {
	kubeClient, omConnectionFactory := mock.NewDefaultFakeClient(m)
	memberClusterMap := getFakeMultiClusterMap(omConnectionFactory)
	return newReplicaSetReconciler(ctx, kubeClient, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, false, false, memberClusterMap, omConnectionFactory.GetConnectionFunc), kubeClient, memberClusterMap, omConnectionFactory
}

func defaultMultiClusterReplicaSetReconciler(ctx context.Context, imageUrls images.ImageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion string, rs *mdbv1.MongoDB) (*ReconcileMongoDbReplicaSet, kubernetesClient.Client, map[string]client.Client, *om.CachedOMConnectionFactory) {
	multiReplicaSetController, client, clusterMap, omConnectionFactory := multiClusterReplicaSetReconciler(ctx, imageUrls, initDatabaseNonStaticImageVersion, databaseNonStaticImageVersion, rs)
	omConnectionFactory.SetPostCreateHook(func(connection om.Connection) {
		connection.(*om.MockedOmConnection).Hostnames = calculateHostNamesForExternalDomains(rs)
	})

	return multiReplicaSetController, client, clusterMap, omConnectionFactory
}
