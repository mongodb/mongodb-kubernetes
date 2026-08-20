package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
)

func directiveReconcilerForTest(elector Elector, objects ...client.Object) (*ReconcileMongoDBDirective, client.Client) {
	c := mock.NewEmptyFakeClientBuilder().WithObjects(objects...).Build()
	return newMongoDBDirectiveReconciler(c, elector), c
}

func buildDirective(m *mdbmulti.MongoDBMultiCluster, clusterName string, term int64, targetSpecHash string) *operatorv1.MongoDBDirective {
	return &operatorv1.MongoDBDirective{
		// non-zero generation so the echo is observable against the zero-valued status
		ObjectMeta: metav1.ObjectMeta{Name: m.Name, Namespace: m.Namespace, Generation: 3},
		Spec: operatorv1.MongoDBDirectiveSpec{
			ClusterName:    clusterName,
			LeadershipTerm: term,
			TargetSpecHash: targetSpecHash,
			MemberCount:    m.Spec.ClusterSpecList[0].Members,
			ClusterIndex:   0,
		},
	}
}

func TestMemberEchoWrittenWhenFencesFail(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()

	t.Run("spec fence fails, echo persisted anyway", func(t *testing.T) {
		directive := buildDirective(m, clusters[0], staticElectorTerm, "not-the-local-hash")
		reconciler, c := directiveReconcilerForTest(NewStaticElector(clusters[0], clusters[1]), directive, m)

		result, err := reconciler.Reconcile(ctx, requestFromObject(directive))
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{RequeueAfter: directiveHoldRetry}, result)

		wantHash := roundTrippedSpecHash(t, m.DeepCopy())
		readBack := operatorv1.MongoDBDirective{}
		require.NoError(t, c.Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &readBack))
		assert.Equal(t, directive.Generation, readBack.Status.ObservedGeneration)
		assert.Equal(t, wantHash, readBack.Status.ObservedSpecHash)
	})

	t.Run("no local CR copy yet", func(t *testing.T) {
		directive := buildDirective(m, clusters[0], staticElectorTerm, "any-hash")
		reconciler, c := directiveReconcilerForTest(NewStaticElector(clusters[0], clusters[1]), directive)

		result, err := reconciler.Reconcile(ctx, requestFromObject(directive))
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{RequeueAfter: directiveHoldRetry}, result)

		readBack := operatorv1.MongoDBDirective{}
		require.NoError(t, c.Get(ctx, kube.ObjectKey(m.Namespace, m.Name), &readBack))
		assert.Equal(t, directive.Generation, readBack.Status.ObservedGeneration)
		assert.Equal(t, "", readBack.Status.ObservedSpecHash)
	})
}

func TestMemberFences(t *testing.T) {
	ctx := context.Background()
	m := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()
	localHash := roundTrippedSpecHash(t, m.DeepCopy())

	// M1 note: a fence hold and the act() hold return the same result — the observable
	// distinction (the staged facts moving) arrives with the materializer. These cases pin the
	// fence *conditions*; the echo assertions prove the pass ran.
	t.Run("stale term holds", func(t *testing.T) {
		directive := buildDirective(m, clusters[0], 3, localHash)
		reconciler, _ := directiveReconcilerForTest(fakeElector{term: 5}, directive, m)

		result, err := reconciler.Reconcile(ctx, requestFromObject(directive))
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{RequeueAfter: directiveHoldRetry}, result)
	})

	t.Run("matching term and hash reaches act", func(t *testing.T) {
		directive := buildDirective(m, clusters[0], 5, localHash)
		reconciler, _ := directiveReconcilerForTest(fakeElector{term: 5}, directive, m)

		result, err := reconciler.Reconcile(ctx, requestFromObject(directive))
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{RequeueAfter: directiveHoldRetry}, result)
	})

	t.Run("newer term passes the fence", func(t *testing.T) {
		// reject the past, not the future: a newer term is a legitimate leader elected while
		// this cluster's locally observed term was stale
		directive := buildDirective(m, clusters[0], 7, localHash)
		reconciler, _ := directiveReconcilerForTest(fakeElector{term: 5}, directive, m)

		result, err := reconciler.Reconcile(ctx, requestFromObject(directive))
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{RequeueAfter: directiveHoldRetry}, result)
	})
}
