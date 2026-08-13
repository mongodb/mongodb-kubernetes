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
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	restclient "k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	runtime_cluster "sigs.k8s.io/controller-runtime/pkg/cluster"

	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
)

// Verifies the hot-reload path end to end against a real API server: a MemberCluster CR
// registers a live cluster entry whose cache serves watches added after the manager has
// started (Watch-after-start), and deleting the CR removes the entry and stops the
// cluster's informers quietly.
//
// TODO(m1kola): master recently gained envtest integration helpers/utilities that are not
// yet available on this branch; migrate this test to them after the next rebase onto the moved trunk.
func TestReconcilerHotReload(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" { //nolint:forbidigo // envtest binaries location
		t.Skip("KUBEBUILDER_ASSETS not set")
	}

	testEnv := &envtest.Environment{
		CRDInstallOptions: envtest.CRDInstallOptions{
			Paths: []string{"../../config/crd/bases/operator.mongodb.com_memberclusters.yaml"},
		},
	}
	cfg, err := testEnv.Start()
	require.NoError(t, err)
	defer func() { assert.NoError(t, testEnv.Stop()) }()

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

	r := NewReconciler(t.Context(), mgr.GetClient(), testNamespace, 10*time.Second, provider, func(restConfig *restclient.Config) (runtime_cluster.Cluster, error) {
		return runtime_cluster.New(restConfig, func(o *runtime_cluster.Options) { o.Scheme = scheme })
	})
	require.NoError(t, r.SetupWithManager(mgr))

	// Derived from t.Context() but cancelled explicitly, before the deferred testEnv.Stop:
	// the API server cannot shut down while the manager is still connected to it.
	mgrCtx, mgrCancel := context.WithCancel(t.Context())
	defer mgrCancel()
	go func() { _ = mgr.Start(mgrCtx) }()
	mgr.GetCache().WaitForCacheSync(mgrCtx)

	centralClient := mgr.GetClient()
	require.NoError(t, centralClient.Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
	}))
	require.NoError(t, centralClient.Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mck-credential-cluster-a", Namespace: testNamespace},
		Data:       map[string][]byte{credentialSecretKubeconfigKey: []byte(envtestKubeconfig(cfg))},
	}))
	require.NoError(t, centralClient.Create(context.Background(), memberClusterCR("cluster-a", "cluster-a", "mck-credential-cluster-a")))

	require.Eventually(t, func() bool {
		_, ok := provider.Entries()["cluster-a"]
		return ok
	}, 10*time.Second, 50*time.Millisecond)

	// The engage hook's watch (added after the manager started) must deliver events.
	require.NoError(t, centralClient.Create(context.Background(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "probe", Namespace: "default"},
	}))
	require.Eventually(t, func() bool { return reconciled.Load() > 0 }, 10*time.Second, 50*time.Millisecond)

	// Delete the CR: the entry goes away and the cluster's informers stop.
	require.NoError(t, centralClient.Delete(context.Background(), memberClusterCR("cluster-a", "", "")))
	require.Eventually(t, func() bool { return len(provider.Entries()) == 0 }, 10*time.Second, 50*time.Millisecond)
	assert.Equal(t, int32(1), removed.Load())

	before := reconciled.Load()
	cm := &corev1.ConfigMap{}
	require.NoError(t, centralClient.Get(context.Background(), types.NamespacedName{Name: "probe", Namespace: "default"}, cm))
	cm.Data = map[string]string{"k": "v"}
	require.NoError(t, centralClient.Update(context.Background(), cm))
	time.Sleep(2 * time.Second)
	assert.Equal(t, before, reconciled.Load(), "no reconcile expected after the member cluster was removed")
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
  name: member
current-context: member
users:
- name: admin
  user:
    client-certificate-data: %s
    client-key-data: %s
`, cfg.Host,
		base64.StdEncoding.EncodeToString(cfg.CAData),
		base64.StdEncoding.EncodeToString(cfg.CertData),
		base64.StdEncoding.EncodeToString(cfg.KeyData))
}
