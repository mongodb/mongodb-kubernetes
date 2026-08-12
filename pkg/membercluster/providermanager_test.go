package membercluster

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	restclient "k8s.io/client-go/rest"

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

func newTestProviderManager(c client.Client, provider *multicluster.Provider, factory *fakeClusterFactory) *providerManager {
	return newProviderManager(c, testNamespace, 7*time.Second, provider, factory.newCluster, context.Background())
}

func TestSyncRegistersMemberCluster(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		credentialSecret("mck-credential-cluster-west", "https://west.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()
	factory := &fakeClusterFactory{}
	hook := &recordedHook{}
	provider.RegisterHooks(context.Background(), hook.hooks())
	m := newTestProviderManager(c, provider, factory)

	// clusterName intentionally differs from metadata.name (e.g. non-RFC-1123 legacy name).
	mc := memberClusterCR("cluster-west", "west_legacy", "mck-credential-cluster-west")
	require.NoError(t, m.sync(context.Background(), mc, zap.S()))

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
	require.NoError(t, m.sync(context.Background(), mc, zap.S()))
	assert.Len(t, factory.clusters, 1)
	assert.Equal(t, []string{"west_legacy"}, hook.added)
}

func TestSyncRebuildsOnSpecChange(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		credentialSecret("mck-credential-cluster-east", "https://east.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()
	factory := &fakeClusterFactory{}
	hook := &recordedHook{}
	provider.RegisterHooks(context.Background(), hook.hooks())
	m := newTestProviderManager(c, provider, factory)

	mc := memberClusterCR("cluster-east", "cluster-east", "mck-credential-cluster-east")
	require.NoError(t, m.sync(context.Background(), mc, zap.S()))

	// Rotate the credential Secret and bump the generation, as a spec change would.
	require.NoError(t, c.Update(context.Background(), credentialSecret("mck-credential-cluster-east", "https://east2.example.com:6443")))
	mc.Generation = 2
	require.NoError(t, m.sync(context.Background(), mc, zap.S()))

	require.Len(t, factory.clusters, 2)
	assert.Equal(t, "https://east2.example.com:6443", factory.configs[1].Host)
	assert.Len(t, provider.Entries(), 1)
	assert.Equal(t, []string{"cluster-east", "cluster-east"}, hook.added)
	assert.Equal(t, []string{"cluster-east"}, hook.removed)
}

func TestRemoveDeregistersEntry(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		credentialSecret("mck-credential-cluster-east", "https://east.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()
	factory := &fakeClusterFactory{}
	hook := &recordedHook{}
	provider.RegisterHooks(context.Background(), hook.hooks())
	m := newTestProviderManager(c, provider, factory)

	require.NoError(t, m.sync(context.Background(), memberClusterCR("cluster-east", "cluster-east", "mck-credential-cluster-east"), zap.S()))
	require.Len(t, provider.Entries(), 1)

	m.remove(context.Background(), "cluster-east", zap.S())
	assert.Empty(t, provider.Entries())
	assert.Equal(t, []string{"cluster-east"}, hook.removed)

	// Removing again (or a CR that was never synced) is a no-op.
	m.remove(context.Background(), "cluster-east", zap.S())
	assert.Equal(t, []string{"cluster-east"}, hook.removed)
}

func TestSyncErrorsWhenCredentialSecretMissing(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme()).Build()
	provider := multicluster.NewProvider()
	m := newTestProviderManager(c, provider, &fakeClusterFactory{})

	mc := memberClusterCR("cluster-bad", "cluster-bad", "mck-credential-cluster-bad")
	require.Error(t, m.sync(context.Background(), mc, zap.S()))
	assert.Empty(t, provider.Entries())

	// Once the Secret appears, the requeued sync registers the cluster.
	require.NoError(t, c.Create(context.Background(), credentialSecret("mck-credential-cluster-bad", "https://bad.example.com:6443")))
	require.NoError(t, m.sync(context.Background(), mc, zap.S()))
	assert.Contains(t, provider.Entries(), "cluster-bad")
}
