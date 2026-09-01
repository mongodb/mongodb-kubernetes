package membercluster

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const testRecheckInterval = 42 * time.Second

func reconcileOnce(t *testing.T, r *Reconciler, name string) ctrl.Result {
	t.Helper()
	result, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: testNamespace}})
	require.NoError(t, err)
	return result
}

func TestReconcileCredentialSecretMissing(t *testing.T) {
	ctx := t.Context()
	central := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(memberClusterCR("cluster-a", "cluster-a", "mck-credential-cluster-a")).Build()
	provider := multicluster.NewProvider()
	r := NewReconciler(ctx, central, testNamespace, testClientTimeout, testRecheckInterval, provider, newTestCluster)

	result := reconcileOnce(t, r, "cluster-a")

	assert.Equal(t, testRecheckInterval, result.RequeueAfter)
	assert.Empty(t, provider.Entries())
}

func TestReconcileCredentialSecretDeletedKeepsEntry(t *testing.T) {
	ctx := t.Context()
	central := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(memberClusterCR("cluster-a", "cluster-a", "mck-credential-cluster-a"), credentialSecret("mck-credential-cluster-a", "https://a.example.com:6443")).Build()
	provider := multicluster.NewProvider()
	r := NewReconciler(ctx, central, testNamespace, testClientTimeout, testRecheckInterval, provider, newTestCluster)

	result := reconcileOnce(t, r, "cluster-a")

	assert.Equal(t, testRecheckInterval, result.RequeueAfter)
	require.Contains(t, provider.Entries(), "cluster-a")
	assert.Equal(t, "https://a.example.com:6443", provider.Entries()["cluster-a"].Cluster.GetConfig().Host)

	// Deleting the credential Secret does not deregister the running entry: the cluster
	// may still be reachable with the credentials loaded earlier.
	secret := &corev1.Secret{}
	require.NoError(t, central.Get(ctx, types.NamespacedName{Name: "mck-credential-cluster-a", Namespace: testNamespace}, secret))
	require.NoError(t, central.Delete(ctx, secret))

	result = reconcileOnce(t, r, "cluster-a")

	assert.Equal(t, testRecheckInterval, result.RequeueAfter)
	assert.Contains(t, provider.Entries(), "cluster-a")
}

func TestReconcileCredentialRotationRebuildsEntry(t *testing.T) {
	ctx := t.Context()
	central := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(memberClusterCR("cluster-a", "cluster-a", "mck-credential-cluster-a"), credentialSecret("mck-credential-cluster-a", "https://a.example.com:6443")).Build()
	provider := multicluster.NewProvider()
	r := NewReconciler(ctx, central, testNamespace, testClientTimeout, testRecheckInterval, provider, newTestCluster)

	reconcileOnce(t, r, "cluster-a")
	require.Contains(t, provider.Entries(), "cluster-a")
	first := provider.Entries()["cluster-a"].Cluster

	// Rotating the credential Secret without touching the CR bumps no generation, but
	// the changed kubeconfig hash must still rebuild the entry.
	secret := &corev1.Secret{}
	require.NoError(t, central.Get(ctx, types.NamespacedName{Name: "mck-credential-cluster-a", Namespace: testNamespace}, secret))
	secret.Data[util.MemberClusterCredentialSecretKubeconfigKey] = []byte(kubeconfig("https://a2.example.com:6443"))
	require.NoError(t, central.Update(ctx, secret))

	result := reconcileOnce(t, r, "cluster-a")

	assert.Equal(t, testRecheckInterval, result.RequeueAfter)
	entry := provider.Entries()["cluster-a"]
	assert.NotSame(t, first, entry.Cluster)
	assert.Equal(t, "https://a2.example.com:6443", entry.Cluster.GetConfig().Host)
}

func TestReconcileDeletedCRRemovesEntry(t *testing.T) {
	ctx := t.Context()
	central := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(memberClusterCR("cluster-a", "cluster-a", "mck-credential-cluster-a"), credentialSecret("mck-credential-cluster-a", "https://a.example.com:6443")).Build()
	provider := multicluster.NewProvider()
	r := NewReconciler(ctx, central, testNamespace, testClientTimeout, testRecheckInterval, provider, newTestCluster)

	reconcileOnce(t, r, "cluster-a")
	require.Contains(t, provider.Entries(), "cluster-a")

	mc := &operatorv1.MemberCluster{}
	require.NoError(t, central.Get(ctx, types.NamespacedName{Name: "cluster-a", Namespace: testNamespace}, mc))
	require.NoError(t, central.Delete(ctx, mc))
	result := reconcileOnce(t, r, "cluster-a")
	assert.Equal(t, ctrl.Result{}, result)
	assert.Empty(t, provider.Entries())
}
