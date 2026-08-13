package membercluster

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	restclient "k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	runtime_cluster "sigs.k8s.io/controller-runtime/pkg/cluster"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/resourcenames"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

// TestMain boots one envtest control plane shared by all tests in this package
// (see test/envtest/env), with only the MemberCluster CRD installed. Future
// envtest-based tests in this package should use env.Shared(t) instead of
// starting their own environment.
func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m, env.WithCRDs("operator.mongodb.com_memberclusters.yaml")))
}

// Verifies the validation state machine end to end against a real API server: a
// MemberCluster CR whose RBAC is missing or outdated stays out of the provider with a
// False RBACValid condition, a fixed RBAC version registers a live cluster entry whose
// cache serves watches added after the manager has started (Watch-after-start), and
// deleting the member ServiceAccount removes the entry again on the periodic re-check.
func TestReconcilerRBACValidation(t *testing.T) {
	cfg := env.Shared(t).Config

	scheme := testScheme()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme})
	require.NoError(t, err)

	provider := multicluster.NewProvider()

	var reconciled atomic.Int32
	c, err := controller.New("membercluster-envtest", mgr, controller.Options{
		Reconciler: reconcile.Func(func(_ context.Context, _ reconcile.Request) (reconcile.Result, error) {
			reconciled.Add(1)
			return reconcile.Result{}, nil
		}),
	})
	require.NoError(t, err)

	var removed atomic.Int32
	provider.RegisterHooks(t.Context(), multicluster.Hooks{
		OnAdd: func(_ context.Context, _ string, entry multicluster.Entry) {
			err := c.Watch(source.Kind[client.Object](entry.Cluster.GetCache(), &corev1.ConfigMap{}, &handler.EnqueueRequestForObject{}))
			assert.NoError(t, err)
		},
		OnRemove: func(_ context.Context, _ string, _ multicluster.Entry) { removed.Add(1) },
	})

	// A short recheck interval makes the periodic re-probe drive the state transitions.
	r := NewReconciler(t.Context(), mgr.GetClient(), testNamespace, 10*time.Second, time.Second, provider, func(restConfig *restclient.Config) (runtime_cluster.Cluster, error) {
		return runtime_cluster.New(restConfig, func(o *runtime_cluster.Options) { o.Scheme = scheme })
	})
	r.validation.expectedVersion = "test-rbac-v1"
	require.NoError(t, r.SetupWithManager(mgr))

	// Derived from t.Context() but cancelled explicitly: the manager must stop before
	// TestMain tears the shared control plane down after m.Run().
	mgrCtx, mgrCancel := context.WithCancel(t.Context())
	defer mgrCancel()
	go func() { _ = mgr.Start(mgrCtx) }()
	mgr.GetCache().WaitForCacheSync(mgrCtx)

	centralClient := mgr.GetClient()
	require.NoError(t, centralClient.Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
	}))
	require.NoError(t, centralClient.Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: testSANamespace},
	}))
	require.NoError(t, centralClient.Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mck-credential-cluster-a", Namespace: testNamespace},
		Data:       map[string][]byte{credentialSecretKubeconfigKey: []byte(envtestKubeconfig(cfg))},
	}))
	require.NoError(t, centralClient.Create(context.Background(), memberClusterCR("cluster-a", "cluster-a", "mck-credential-cluster-a")))

	rbacValid := func() *metav1.Condition {
		mc := &operatorv1.MemberCluster{}
		if err := centralClient.Get(context.Background(), types.NamespacedName{Name: "cluster-a", Namespace: testNamespace}, mc); err != nil {
			return nil
		}
		return apimeta.FindStatusCondition(mc.Status.Conditions, operatorv1.MemberClusterConditionRBACValid)
	}

	// No member ServiceAccount yet: the cluster is skipped and reports it.
	require.Eventually(t, func() bool {
		condition := rbacValid()
		return condition != nil && condition.Status == metav1.ConditionFalse && condition.Reason == reasonMemberServiceAccountNotFound
	}, 10*time.Second, 50*time.Millisecond)
	assert.Empty(t, provider.Entries())

	// An outdated RBAC version is still invalid.
	require.NoError(t, centralClient.Create(context.Background(), &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:        resourcenames.MemberClusterServiceAccountName("cluster-a"),
			Namespace:   testSANamespace,
			Annotations: map[string]string{util.MemberClusterRBACVersionAnnotation: "outdated"},
		},
	}))
	require.Eventually(t, func() bool {
		condition := rbacValid()
		return condition != nil && condition.Status == metav1.ConditionFalse && condition.Reason == reasonVersionMismatch
	}, 10*time.Second, 50*time.Millisecond)
	assert.Empty(t, provider.Entries())

	// Reapplying the expected RBAC version registers the entry on the next re-check.
	sa := &corev1.ServiceAccount{}
	require.NoError(t, centralClient.Get(context.Background(), types.NamespacedName{
		Name:      resourcenames.MemberClusterServiceAccountName("cluster-a"),
		Namespace: testSANamespace,
	}, sa))
	sa.Annotations[util.MemberClusterRBACVersionAnnotation] = "test-rbac-v1"
	require.NoError(t, centralClient.Update(context.Background(), sa))

	require.Eventually(t, func() bool {
		_, ok := provider.Entries()["cluster-a"]
		return ok
	}, 10*time.Second, 50*time.Millisecond)
	require.Eventually(t, func() bool {
		condition := rbacValid()
		return condition != nil && condition.Status == metav1.ConditionTrue && condition.Reason == reasonValid
	}, 10*time.Second, 50*time.Millisecond)

	// The engage hook's watch (added after the manager started) must deliver events.
	require.NoError(t, centralClient.Create(context.Background(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "probe", Namespace: "default"},
	}))
	require.Eventually(t, func() bool { return reconciled.Load() > 0 }, 10*time.Second, 50*time.Millisecond)

	// Delete the member ServiceAccount: the periodic re-check deregisters the entry and
	// the cluster's informers stop.
	require.NoError(t, centralClient.Delete(context.Background(), sa))
	require.Eventually(t, func() bool { return len(provider.Entries()) == 0 }, 10*time.Second, 50*time.Millisecond)
	assert.Equal(t, int32(1), removed.Load())
	require.Eventually(t, func() bool {
		condition := rbacValid()
		return condition != nil && condition.Status == metav1.ConditionFalse && condition.Reason == reasonMemberServiceAccountNotFound
	}, 10*time.Second, 50*time.Millisecond)

	before := reconciled.Load()
	cm := &corev1.ConfigMap{}
	require.NoError(t, centralClient.Get(context.Background(), types.NamespacedName{Name: "probe", Namespace: "default"}, cm))
	cm.Data = map[string]string{"k": "v"}
	require.NoError(t, centralClient.Update(context.Background(), cm))
	time.Sleep(2 * time.Second)
	assert.Equal(t, before, reconciled.Load(), "no reconcile expected after the member cluster was removed")

	// Deleting the CR with no entry registered is a no-op.
	require.NoError(t, centralClient.Delete(context.Background(), memberClusterCR("cluster-a", "", "")))
	assert.Equal(t, int32(1), removed.Load())
}

func envtestKubeconfig(cfg *restclient.Config) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: %s
    certificate-authority-data: %s
  name: member
contexts:
- context:
    cluster: member
    user: admin
    namespace: %s
  name: member
current-context: member
users:
- name: admin
  user:
    client-certificate-data: %s
    client-key-data: %s
`, cfg.Host,
		base64.StdEncoding.EncodeToString(cfg.CAData),
		testSANamespace,
		base64.StdEncoding.EncodeToString(cfg.CertData),
		base64.StdEncoding.EncodeToString(cfg.KeyData))
}
