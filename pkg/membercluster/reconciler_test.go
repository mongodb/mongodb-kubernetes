package membercluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/resourcenames"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const testRecheckInterval = 42 * time.Second

// newTestReconciler wires a Reconciler whose validator probes a fake member-cluster client
// and whose expected version is pinned, independent of the build-time injected default.
func newTestReconciler(ctx context.Context, centralClient client.Client, memberClient client.Client, provider *multicluster.Provider) *Reconciler {
	r := NewReconciler(ctx, centralClient, testNamespace, testClientTimeout, testRecheckInterval, provider, newTestCluster)
	if memberClient != nil {
		r.validator = staticValidator(memberClient)
	}
	r.validator.expectedVersion = testExpected
	return r
}

func reconcileOnce(t *testing.T, r *Reconciler, name string) ctrl.Result {
	t.Helper()
	result, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: testNamespace}})
	require.NoError(t, err)
	return result
}

func rbacValidCondition(t *testing.T, c client.Client, name string) *metav1.Condition {
	t.Helper()
	mc := &operatorv1.MemberCluster{}
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Name: name, Namespace: testNamespace}, mc))
	return apimeta.FindStatusCondition(mc.Status.Conditions, operatorv1.MemberClusterConditionRBACValid)
}

func TestReconcileCredentialSecretMissing(t *testing.T) {
	ctx := t.Context()
	central := fake.NewClientBuilder().WithScheme(testScheme()).WithStatusSubresource(&operatorv1.MemberCluster{}).
		WithObjects(memberClusterCR("cluster-a", "cluster-a", "mck-credential-cluster-a")).Build()
	provider := multicluster.NewProvider()
	r := newTestReconciler(ctx, central, nil, provider)

	result := reconcileOnce(t, r, "cluster-a")

	assert.Equal(t, testRecheckInterval, result.RequeueAfter)
	assert.Empty(t, provider.Entries())
	condition := rbacValidCondition(t, central, "cluster-a")
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, reasonInvalid, condition.Reason)
	assert.Contains(t, condition.Message, "mck-credential-cluster-a")
}

func TestReconcileHappyPathRegistersEntry(t *testing.T) {
	ctx := t.Context()
	mc := memberClusterCR("cluster-a", "cluster-a", "mck-credential-cluster-a")
	central := fake.NewClientBuilder().WithScheme(testScheme()).WithStatusSubresource(&operatorv1.MemberCluster{}).
		WithObjects(mc, credentialSecret("mck-credential-cluster-a", "https://a.example.com:6443")).Build()
	member := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		memberServiceAccount(map[string]string{util.MemberClusterRBACVersionAnnotation: testExpected}),
	).Build()
	provider := multicluster.NewProvider()
	r := newTestReconciler(ctx, central, member, provider)

	result := reconcileOnce(t, r, "cluster-a")

	assert.Equal(t, testRecheckInterval, result.RequeueAfter)
	require.Contains(t, provider.Entries(), "cluster-a")
	assert.Equal(t, "https://a.example.com:6443", provider.Entries()["cluster-a"].Cluster.GetConfig().Host)
	condition := rbacValidCondition(t, central, "cluster-a")
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionTrue, condition.Status)
	assert.Equal(t, reasonValid, condition.Reason)
}

func TestReconcileDoesNotRewriteUnchangedCondition(t *testing.T) {
	ctx := t.Context()
	central := fake.NewClientBuilder().WithScheme(testScheme()).WithStatusSubresource(&operatorv1.MemberCluster{}).
		WithObjects(memberClusterCR("cluster-a", "cluster-a", "mck-credential-cluster-a"), credentialSecret("mck-credential-cluster-a", "https://a.example.com:6443")).Build()
	member := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		memberServiceAccount(map[string]string{util.MemberClusterRBACVersionAnnotation: testExpected}),
	).Build()
	provider := multicluster.NewProvider()
	r := newTestReconciler(ctx, central, member, provider)

	reconcileOnce(t, r, "cluster-a")
	first := &operatorv1.MemberCluster{}
	require.NoError(t, central.Get(ctx, types.NamespacedName{Name: "cluster-a", Namespace: testNamespace}, first))

	result := reconcileOnce(t, r, "cluster-a")
	second := &operatorv1.MemberCluster{}
	require.NoError(t, central.Get(ctx, types.NamespacedName{Name: "cluster-a", Namespace: testNamespace}, second))

	assert.Equal(t, testRecheckInterval, result.RequeueAfter)
	assert.Equal(t, first.ResourceVersion, second.ResourceVersion, "an unchanged condition must not be patched")
	// The periodic re-probe must not churn the provider entry either.
	assert.Contains(t, provider.Entries(), "cluster-a")
}

func TestReconcileInvalidRBACRemovesEntry(t *testing.T) {
	ctx := t.Context()
	central := fake.NewClientBuilder().WithScheme(testScheme()).WithStatusSubresource(&operatorv1.MemberCluster{}).
		WithObjects(memberClusterCR("cluster-a", "cluster-a", "mck-credential-cluster-a"), credentialSecret("mck-credential-cluster-a", "https://a.example.com:6443")).Build()
	member := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		memberServiceAccount(map[string]string{util.MemberClusterRBACVersionAnnotation: "0.0.1"}),
	).Build()
	provider := multicluster.NewProvider()
	r := newTestReconciler(ctx, central, member, provider)

	reconcileOnce(t, r, "cluster-a")
	condition := rbacValidCondition(t, central, "cluster-a")
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, reasonInvalid, condition.Reason)
	assert.Empty(t, provider.Entries())

	// A previously-registered entry is removed once RBAC turns definitively invalid.
	setMemberRBACVersion(t, ctx, member, testExpected)
	reconcileOnce(t, r, "cluster-a")
	require.Contains(t, provider.Entries(), "cluster-a")
	setMemberRBACVersion(t, ctx, member, "0.0.1")
	reconcileOnce(t, r, "cluster-a")
	assert.Empty(t, provider.Entries())
}

func setMemberRBACVersion(t *testing.T, ctx context.Context, memberClient client.Client, version string) {
	t.Helper()
	sa := &corev1.ServiceAccount{}
	require.NoError(t, memberClient.Get(ctx, types.NamespacedName{Name: testSAName, Namespace: testSANamespace}, sa))
	sa.Annotations[util.MemberClusterRBACVersionAnnotation] = version
	require.NoError(t, memberClient.Update(ctx, sa))
}

func TestReconcileProbeFailureKeepsEntry(t *testing.T) {
	ctx := t.Context()
	central := fake.NewClientBuilder().WithScheme(testScheme()).WithStatusSubresource(&operatorv1.MemberCluster{}).
		WithObjects(memberClusterCR("cluster-a", "cluster-a", "mck-credential-cluster-a"), credentialSecret("mck-credential-cluster-a", "https://a.example.com:6443")).Build()
	// No ServiceAccount reachable is False; a validator that cannot even reach the cluster
	// is Unknown and must keep the entry.
	member := fake.NewClientBuilder().WithScheme(testScheme()).WithInterceptorFuncs(getErrorInterceptors(errors.New("connection refused"))).Build()
	provider := multicluster.NewProvider()
	r := newTestReconciler(ctx, central, member, provider)

	reconcileOnce(t, r, "cluster-a")

	assert.Contains(t, provider.Entries(), "cluster-a")
	condition := rbacValidCondition(t, central, "cluster-a")
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionUnknown, condition.Status)
	assert.Equal(t, reasonProbeFailed, condition.Reason)
}

func TestReconcileValidationDisabled(t *testing.T) {
	ctx := t.Context()
	central := fake.NewClientBuilder().WithScheme(testScheme()).WithStatusSubresource(&operatorv1.MemberCluster{}).
		WithObjects(memberClusterCR("cluster-a", "cluster-a", "mck-credential-cluster-a"), credentialSecret("mck-credential-cluster-a", "https://a.example.com:6443")).Build()
	provider := multicluster.NewProvider()
	// No member client: a validator probe would fail, so a registered entry proves the
	// probe was skipped.
	r := newTestReconciler(ctx, central, nil, provider)
	r.validator.expectedVersion = ""

	reconcileOnce(t, r, "cluster-a")

	assert.Contains(t, provider.Entries(), "cluster-a")
	condition := rbacValidCondition(t, central, "cluster-a")
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionTrue, condition.Status)
	assert.Equal(t, reasonValid, condition.Reason)
}

func TestReconcileDeletedCRRemovesEntry(t *testing.T) {
	ctx := t.Context()
	central := fake.NewClientBuilder().WithScheme(testScheme()).WithStatusSubresource(&operatorv1.MemberCluster{}).
		WithObjects(memberClusterCR("cluster-a", "cluster-a", "mck-credential-cluster-a"), credentialSecret("mck-credential-cluster-a", "https://a.example.com:6443")).Build()
	member := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		memberServiceAccount(map[string]string{util.MemberClusterRBACVersionAnnotation: testExpected}),
	).Build()
	provider := multicluster.NewProvider()
	r := newTestReconciler(ctx, central, member, provider)

	reconcileOnce(t, r, "cluster-a")
	require.Contains(t, provider.Entries(), "cluster-a")

	mc := &operatorv1.MemberCluster{}
	require.NoError(t, central.Get(ctx, types.NamespacedName{Name: "cluster-a", Namespace: testNamespace}, mc))
	require.NoError(t, central.Delete(ctx, mc))
	result := reconcileOnce(t, r, "cluster-a")
	assert.Equal(t, ctrl.Result{}, result)
	assert.Empty(t, provider.Entries())
}

// memberServiceAccountNameDocumentsTheContract pins the probe target the Reconciler passes
// to the validator: the member operator SA derived from the CR's metadata.name.
func TestReconcileProbesMemberServiceAccount(t *testing.T) {
	ctx := t.Context()
	central := fake.NewClientBuilder().WithScheme(testScheme()).WithStatusSubresource(&operatorv1.MemberCluster{}).
		WithObjects(memberClusterCR("cluster-a", "cluster-a", "mck-credential-cluster-a"), credentialSecret("mck-credential-cluster-a", "https://a.example.com:6443")).Build()
	member := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(&corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:        resourcenames.MemberClusterServiceAccountName("cluster-a"),
			Namespace:   "mongodb",
			Annotations: map[string]string{util.MemberClusterRBACVersionAnnotation: testExpected},
		},
	}).Build()
	provider := multicluster.NewProvider()
	r := newTestReconciler(ctx, central, member, provider)

	reconcileOnce(t, r, "cluster-a")
	condition := rbacValidCondition(t, central, "cluster-a")
	require.NotNil(t, condition)
	assert.Equal(t, metav1.ConditionTrue, condition.Status)
}
