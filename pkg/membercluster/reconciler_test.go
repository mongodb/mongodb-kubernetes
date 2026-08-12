package membercluster

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	restclient "k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
)

const testNamespace = "test-ns"

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = operatorv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	return s
}

func kubeconfig(server string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: %s
    insecure-skip-tls-verify: true
  name: member
contexts:
- context:
    cluster: member
    user: mck-operator
    namespace: mongodb
  name: member
current-context: member
users:
- name: mck-operator
  user:
    token: a-token
`, server)
}

func memberClusterCR(name, clusterName, secretName string) *operatorv1.MemberCluster {
	return &operatorv1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: operatorv1.MemberClusterSpec{
			ClusterName:         clusterName,
			CredentialSecretRef: corev1.LocalObjectReference{Name: secretName},
		},
	}
}

func credentialSecret(name, server string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Data:       map[string][]byte{credentialSecretKubeconfigKey: []byte(kubeconfig(server))},
	}
}

type fakeClusterFactory struct {
	clusters []*multicluster.MockedCluster
	configs  []*restclient.Config
}

func (f *fakeClusterFactory) newCluster(restConfig *restclient.Config) (cluster.Cluster, error) {
	c := multicluster.New(nil)
	f.clusters = append(f.clusters, c)
	f.configs = append(f.configs, restConfig)
	return c, nil
}

type recordedHook struct {
	added   []string
	removed []string
}

func (h *recordedHook) hooks() multicluster.Hooks {
	return multicluster.Hooks{
		OnAdd: func(_ context.Context, clusterName string, _ multicluster.Entry) {
			h.added = append(h.added, clusterName)
		},
		OnRemove: func(_ context.Context, clusterName string, _ multicluster.Entry) {
			h.removed = append(h.removed, clusterName)
		},
	}
}

func newTestReconciler(c client.Client, provider *multicluster.Provider, factory *fakeClusterFactory) *Reconciler {
	return NewReconciler(c, testNamespace, 7*time.Second, provider, factory.newCluster, context.Background())
}

func reconcileCR(t *testing.T, r *Reconciler, name string) (ctrl.Result, error) {
	t.Helper()
	return r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: testNamespace}})
}

func TestReconcileRegistersMemberCluster(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		memberClusterCR("cluster-west", "west_legacy", "mck-credential-cluster-west"),
		credentialSecret("mck-credential-cluster-west", "https://west.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()
	factory := &fakeClusterFactory{}
	hook := &recordedHook{}
	provider.RegisterHooks(context.Background(), hook.hooks())
	r := newTestReconciler(c, provider, factory)

	_, err := reconcileCR(t, r, "cluster-west")
	require.NoError(t, err)

	entries := provider.Entries()
	require.Len(t, entries, 1)
	// keyed by spec.clusterName, not metadata.name
	entry := entries["west_legacy"]
	assert.Equal(t, "cluster-west", entry.ResourceName)
	assert.Equal(t, "https://west.example.com:6443", factory.configs[0].Host)
	assert.Equal(t, 7*time.Second, factory.configs[0].Timeout)
	assert.Equal(t, []string{"west_legacy"}, hook.added)
	assert.Empty(t, hook.removed)

	// A resync (same generation) must not rebuild the entry.
	_, err = reconcileCR(t, r, "cluster-west")
	require.NoError(t, err)
	assert.Len(t, factory.clusters, 1)
	assert.Equal(t, []string{"west_legacy"}, hook.added)
}

func TestReconcileRebuildsOnSpecChange(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		memberClusterCR("cluster-east", "cluster-east", "mck-credential-cluster-east"),
		credentialSecret("mck-credential-cluster-east", "https://east.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()
	factory := &fakeClusterFactory{}
	hook := &recordedHook{}
	provider.RegisterHooks(context.Background(), hook.hooks())
	r := newTestReconciler(c, provider, factory)

	_, err := reconcileCR(t, r, "cluster-east")
	require.NoError(t, err)

	// Rotate the credential Secret and bump the generation, as a spec change would.
	require.NoError(t, c.Update(context.Background(), credentialSecret("mck-credential-cluster-east", "https://east2.example.com:6443")))
	mc := &operatorv1.MemberCluster{}
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "cluster-east", Namespace: testNamespace}, mc))
	mc.Generation = mc.Generation + 1
	require.NoError(t, c.Update(context.Background(), mc))

	_, err = reconcileCR(t, r, "cluster-east")
	require.NoError(t, err)

	require.Len(t, factory.clusters, 2)
	assert.Equal(t, "https://east2.example.com:6443", factory.configs[1].Host)
	assert.Len(t, provider.Entries(), 1)
	assert.Equal(t, []string{"cluster-east", "cluster-east"}, hook.added)
	assert.Equal(t, []string{"cluster-east"}, hook.removed)
}

func TestReconcileRemovesEntryOnDelete(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		memberClusterCR("cluster-east", "cluster-east", "mck-credential-cluster-east"),
		credentialSecret("mck-credential-cluster-east", "https://east.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()
	factory := &fakeClusterFactory{}
	hook := &recordedHook{}
	provider.RegisterHooks(context.Background(), hook.hooks())
	r := newTestReconciler(c, provider, factory)

	_, err := reconcileCR(t, r, "cluster-east")
	require.NoError(t, err)
	require.Len(t, provider.Entries(), 1)

	require.NoError(t, c.Delete(context.Background(), memberClusterCR("cluster-east", "", "")))

	_, err = reconcileCR(t, r, "cluster-east")
	require.NoError(t, err)
	assert.Empty(t, provider.Entries())
	assert.Equal(t, []string{"cluster-east"}, hook.removed)

	// Deleting again (or a CR that was never reconciled) is a no-op.
	_, err = reconcileCR(t, r, "cluster-east")
	require.NoError(t, err)
	assert.Equal(t, []string{"cluster-east"}, hook.removed)
}

func TestReconcileErrorsWhenCredentialSecretMissing(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		memberClusterCR("cluster-bad", "cluster-bad", "mck-credential-cluster-bad"),
	).Build()
	provider := multicluster.NewProvider()
	r := newTestReconciler(c, provider, &fakeClusterFactory{})

	_, err := reconcileCR(t, r, "cluster-bad")
	require.Error(t, err)
	assert.Empty(t, provider.Entries())

	// Once the Secret appears, the requeued reconcile registers the cluster.
	require.NoError(t, c.Create(context.Background(), credentialSecret("mck-credential-cluster-bad", "https://bad.example.com:6443")))
	_, err = reconcileCR(t, r, "cluster-bad")
	require.NoError(t, err)
	assert.Contains(t, provider.Entries(), "cluster-bad")
}
