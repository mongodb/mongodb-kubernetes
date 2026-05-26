package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbmulti "github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
)

func TestSecondary_NoActionWhenLocalClusterMissingFromSpec(t *testing.T) {
	ctx := context.Background()

	mrs := &mdbmulti.MongoDBMultiCluster{}
	mrs.Name, mrs.Namespace = "mdb", "ns"
	mrs.Spec.ClusterSpecList = mdb.ClusterSpecList{
		{ClusterName: "A", Members: 1},
		{ClusterName: "C", Members: 1},
	}

	// multiReplicaSetReconciler calls NewDefaultFakeClient(mrs), which already
	// registers mrs via WithObjects — no separate Create call needed.
	r, kubeClient, _, _ := multiReplicaSetReconciler(ctx, images.ImageUrls{}, "", "", mrs)
	r.localClusterName = "B"

	result, err := r.reconcileSecondary(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "mdb", Namespace: "ns"},
	}, zap.S())
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)

	sts := &appsv1.StatefulSetList{}
	require.NoError(t, kubeClient.List(ctx, sts))
	assert.Empty(t, sts.Items, "no StatefulSet should be created when local cluster is not in spec")
}
