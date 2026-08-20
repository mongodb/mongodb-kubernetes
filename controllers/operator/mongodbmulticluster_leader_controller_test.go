package operator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
)

// fakeElector returns a fixed leadership belief, for injecting arbitrary terms in tests.
type fakeElector struct {
	term     int64
	isLeader bool
}

func (e fakeElector) Current(types.NamespacedName) (term int64, isLeader bool) {
	return e.term, e.isLeader
}

// leaderReconcilerForTest builds a leader reconciler over one fake client per member cluster,
// with the CR seeded into the self cluster's client (the operator's local API server).
func leaderReconcilerForTest(m *mdbmulti.MongoDBMultiCluster, self string, elector Elector) (*ReconcileMongoDBMultiClusterLeader, map[string]client.Client) {
	clientsMap := make(map[string]client.Client)
	for _, clusterName := range clusters {
		if clusterName == self {
			clientsMap[clusterName] = mock.NewEmptyFakeClientBuilder().WithObjects(m).Build()
		} else {
			clientsMap[clusterName] = mock.NewEmptyFakeClientBuilder().Build()
		}
	}
	return newMongoDBMultiClusterLeaderReconciler(clientsMap[self], clientsMap, elector), clientsMap
}

func TestLeaderWritesDirectivesToAllClusters(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, clientsMap := leaderReconcilerForTest(m, clusters[0], NewStaticElector(clusters[0], clusters[0]))

	result, err := reconciler.Reconcile(ctx, requestFromObject(m))
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{RequeueAfter: 10 * time.Second}, result)

	wantHash := mustSpecHash(t, m.Spec)
	wantAllocations := map[string]int{clusters[0]: 0, clusters[1]: 1, clusters[2]: 2}
	for i, item := range m.Spec.ClusterSpecList {
		directive := operatorv1.MongoDBDirective{}
		require.NoError(t, clientsMap[item.ClusterName].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &directive))
		assert.Equal(t, item.ClusterName, directive.Spec.ClusterName)
		assert.Equal(t, staticElectorTerm, directive.Spec.LeadershipTerm)
		assert.Equal(t, wantHash, directive.Spec.TargetSpecHash)
		assert.Equal(t, item.Members, directive.Spec.MemberCount)
		assert.Equal(t, i, directive.Spec.ClusterIndex)
		assert.Equal(t, wantAllocations, directive.Spec.IndexAllocations)
	}

	require.NoError(t, clientsMap[clusters[0]].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), m))
	assert.Equal(t, status.PhasePending, m.Status.Phase)

	// second pass is idempotent: the directives exist, so the update path runs
	_, err = reconciler.Reconcile(ctx, requestFromObject(m))
	require.NoError(t, err)
}

func TestNonLeaderWritesNothing(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, clientsMap := leaderReconcilerForTest(m, clusters[1], NewStaticElector(clusters[1], clusters[0]))

	result, err := reconciler.Reconcile(ctx, requestFromObject(m))
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)

	for clusterName, c := range clientsMap {
		err := c.Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &operatorv1.MongoDBDirective{})
		assert.True(t, apiErrors.IsNotFound(err), "no directive expected on cluster %s", clusterName)
	}

	require.NoError(t, clientsMap[clusters[1]].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), m))
	assert.Equal(t, status.Phase(""), m.Status.Phase)
}

func TestElectorTermFlowsIntoDirectiveSpec(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	reconciler, clientsMap := leaderReconcilerForTest(m, clusters[0], fakeElector{term: 42, isLeader: true})

	_, err := reconciler.Reconcile(ctx, requestFromObject(m))
	require.NoError(t, err)

	for _, item := range m.Spec.ClusterSpecList {
		directive := operatorv1.MongoDBDirective{}
		require.NoError(t, clientsMap[item.ClusterName].Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &directive))
		assert.Equal(t, int64(42), directive.Spec.LeadershipTerm)
	}
}
