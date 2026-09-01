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
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	testNamespace     = "test-ns"
	testClientTimeout = 7 * time.Second
)

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

func withGeneration(mc *operatorv1.MemberCluster, generation int64) *operatorv1.MemberCluster {
	mc.Generation = generation
	return mc
}

func credentialSecret(name, server string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Data:       map[string][]byte{util.MemberClusterCredentialSecretKubeconfigKey: []byte(kubeconfig(server))},
	}
}

// testCluster makes the rest.Config a cluster was built with observable through
// cluster.Cluster.GetConfig, so tests assert on the provider's entries instead of
// inspecting the cluster factory's internals.
type testCluster struct {
	*multicluster.MockedCluster
	config *restclient.Config
}

func (c *testCluster) GetConfig() *restclient.Config {
	return c.config
}

func newTestCluster(restConfig *restclient.Config) (cluster.Cluster, error) {
	return &testCluster{MockedCluster: multicluster.New(nil), config: restConfig}, nil
}

type recordedHook struct {
	events []string
}

func (h *recordedHook) hooks() multicluster.Hooks {
	return multicluster.Hooks{
		OnAdd: func(_ context.Context, clusterName string, _ multicluster.Entry) {
			h.events = append(h.events, "add:"+clusterName)
		},
		OnRemove: func(_ context.Context, clusterName string, _ multicluster.Entry) {
			h.events = append(h.events, "remove:"+clusterName)
		},
	}
}

func newTestProviderManager(ctx context.Context, c client.Reader, provider *multicluster.Provider) *providerManager {
	return newProviderManager(ctx, c, testNamespace, testClientTimeout, provider, newTestCluster)
}

func mustLoadCredentials(t *testing.T, ctx context.Context, m *providerManager, mc *operatorv1.MemberCluster) *credentials {
	t.Helper()
	creds, err := m.loadCredentials(ctx, mc)
	require.NoError(t, err)
	return creds
}

func TestEnsureRegistersMemberCluster(t *testing.T) {
	ctx := t.Context()
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		credentialSecret("mck-credential-cluster-west", "https://west.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()
	hook := &recordedHook{}
	provider.RegisterHooks(ctx, hook.hooks())
	m := newTestProviderManager(ctx, c, provider)

	// clusterName intentionally differs from metadata.name (e.g. non-RFC-1123 legacy name).
	mc := memberClusterCR("cluster-west", "west_legacy", "mck-credential-cluster-west")
	creds := mustLoadCredentials(t, ctx, m, mc)
	assert.Equal(t, testClientTimeout, creds.restConfig.Timeout)
	require.NoError(t, m.ensure(ctx, mc, creds, zap.S()))

	entries := provider.Entries()
	require.Len(t, entries, 1)
	// keyed by spec.clusterName, not metadata.name
	entry := entries["west_legacy"]
	assert.Equal(t, "cluster-west", entry.ResourceName)
	config := entry.Cluster.GetConfig()
	require.NotNil(t, config)
	assert.Equal(t, "https://west.example.com:6443", config.Host)
	assert.Equal(t, testClientTimeout, config.Timeout)
	assert.Equal(t, []string{"add:west_legacy"}, hook.events)

	// A resync (same generation, same credentials) must not rebuild the entry.
	creds = mustLoadCredentials(t, ctx, m, mc)
	require.NoError(t, m.ensure(ctx, mc, creds, zap.S()))
	assert.Same(t, entry.Cluster, provider.Entries()["west_legacy"].Cluster)
	assert.Equal(t, []string{"add:west_legacy"}, hook.events)
}

func TestEnsureRebuildsOnSpecChange(t *testing.T) {
	ctx := t.Context()
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		credentialSecret("mck-credential-cluster-east", "https://east.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()
	hook := &recordedHook{}
	provider.RegisterHooks(ctx, hook.hooks())
	m := newTestProviderManager(ctx, c, provider)

	mc := memberClusterCR("cluster-east", "cluster-east", "mck-credential-cluster-east")
	require.NoError(t, m.ensure(ctx, mc, mustLoadCredentials(t, ctx, m, mc), zap.S()))

	// Rotate the credential Secret and bump the generation, as a spec change would.
	require.NoError(t, c.Update(ctx, credentialSecret("mck-credential-cluster-east", "https://east2.example.com:6443")))
	require.NoError(t, m.ensure(ctx, withGeneration(mc, 2), mustLoadCredentials(t, ctx, m, mc), zap.S()))

	// Still exactly one entry, now pointing at the new host.
	entries := provider.Entries()
	require.Len(t, entries, 1)
	config := entries["cluster-east"].Cluster.GetConfig()
	require.NotNil(t, config)
	assert.Equal(t, "https://east2.example.com:6443", config.Host)
	// OnRemove releases per-cluster state before OnAdd re-attaches on rebuild.
	assert.Equal(t, []string{"add:cluster-east", "remove:cluster-east", "add:cluster-east"}, hook.events)
}

func TestEnsureRebuildsOnCredentialRotation(t *testing.T) {
	ctx := t.Context()
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		credentialSecret("mck-credential-cluster-east", "https://east.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()
	hook := &recordedHook{}
	provider.RegisterHooks(ctx, hook.hooks())
	m := newTestProviderManager(ctx, c, provider)

	mc := memberClusterCR("cluster-east", "cluster-east", "mck-credential-cluster-east")
	require.NoError(t, m.ensure(ctx, mc, mustLoadCredentials(t, ctx, m, mc), zap.S()))

	// Rotate the credential Secret without touching the CR: the changed hash alone must
	// rebuild the entry.
	require.NoError(t, c.Update(ctx, credentialSecret("mck-credential-cluster-east", "https://east2.example.com:6443")))
	require.NoError(t, m.ensure(ctx, mc, mustLoadCredentials(t, ctx, m, mc), zap.S()))

	entries := provider.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, "https://east2.example.com:6443", entries["cluster-east"].Cluster.GetConfig().Host)
	assert.Equal(t, []string{"add:cluster-east", "remove:cluster-east", "add:cluster-east"}, hook.events)
}

func TestRemoveDeregistersEntry(t *testing.T) {
	ctx := t.Context()
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		credentialSecret("mck-credential-cluster-east", "https://east.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()
	hook := &recordedHook{}
	provider.RegisterHooks(ctx, hook.hooks())
	m := newTestProviderManager(ctx, c, provider)

	mc := memberClusterCR("cluster-east", "cluster-east", "mck-credential-cluster-east")
	require.NoError(t, m.ensure(ctx, mc, mustLoadCredentials(t, ctx, m, mc), zap.S()))
	require.Len(t, provider.Entries(), 1)

	m.remove(ctx, "cluster-east", zap.S())
	assert.Empty(t, provider.Entries())
	assert.Equal(t, []string{"add:cluster-east", "remove:cluster-east"}, hook.events)

	// Removing again (or a CR that was never registered) is a no-op.
	m.remove(ctx, "cluster-east", zap.S())
	assert.Equal(t, []string{"add:cluster-east", "remove:cluster-east"}, hook.events)
}

func TestLoadCredentialsErrors(t *testing.T) {
	tests := []struct {
		name          string
		secret        *corev1.Secret
		wantErrSubstr string
	}{
		{
			name:          "secret missing",
			secret:        nil,
			wantErrSubstr: `reading credential secret "mck-credential-cluster-bad"`,
		},
		{
			name: "kubeconfig key missing",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "mck-credential-cluster-bad", Namespace: testNamespace},
				Data:       map[string][]byte{"other": []byte("x")},
			},
			wantErrSubstr: `credential secret "mck-credential-cluster-bad" has no "kubeconfig" key`,
		},
		{
			name: "kubeconfig malformed",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "mck-credential-cluster-bad", Namespace: testNamespace},
				Data:       map[string][]byte{util.MemberClusterCredentialSecretKubeconfigKey: []byte("not: [valid")},
			},
			wantErrSubstr: `building REST config from credential secret`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			builder := fake.NewClientBuilder().WithScheme(testScheme())
			if tt.secret != nil {
				builder = builder.WithObjects(tt.secret)
			}
			m := newTestProviderManager(ctx, builder.Build(), multicluster.NewProvider())

			mc := memberClusterCR("cluster-bad", "cluster-bad", "mck-credential-cluster-bad")
			_, err := m.loadCredentials(ctx, mc)
			require.ErrorContains(t, err, tt.wantErrSubstr)
			assert.Empty(t, m.provider.Entries())
		})
	}
}

func TestLoadCredentialsRecoversWhenSecretAppears(t *testing.T) {
	ctx := t.Context()
	c := fake.NewClientBuilder().WithScheme(testScheme()).Build()
	provider := multicluster.NewProvider()
	m := newTestProviderManager(ctx, c, provider)

	mc := memberClusterCR("cluster-bad", "cluster-bad", "mck-credential-cluster-bad")
	_, err := m.loadCredentials(ctx, mc)
	require.ErrorContains(t, err, `reading credential secret "mck-credential-cluster-bad"`)

	// Once the Secret appears, the requeued reconcile registers the cluster.
	require.NoError(t, c.Create(ctx, credentialSecret("mck-credential-cluster-bad", "https://bad.example.com:6443")))
	require.NoError(t, m.ensure(ctx, mc, mustLoadCredentials(t, ctx, m, mc), zap.S()))
	assert.Contains(t, provider.Entries(), "cluster-bad")
}

func TestEnsureRefusesDuplicateClusterName(t *testing.T) {
	ctx := t.Context()
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		credentialSecret("mck-credential-a", "https://a.example.com:6443"),
		credentialSecret("mck-credential-b", "https://b.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()
	hook := &recordedHook{}
	provider.RegisterHooks(ctx, hook.hooks())
	m := newTestProviderManager(ctx, c, provider)

	mcA := memberClusterCR("cr-a", "shared-name", "mck-credential-a")
	require.NoError(t, m.ensure(ctx, mcA, mustLoadCredentials(t, ctx, m, mcA), zap.S()))

	// A second CR claiming the same clusterName is refused: first writer wins.
	mcB := memberClusterCR("cr-b", "shared-name", "mck-credential-b")
	err := m.ensure(ctx, mcB, mustLoadCredentials(t, ctx, m, mcB), zap.S())
	require.ErrorContains(t, err, `clusterName "shared-name" is already registered by MemberCluster "cr-a"`)
	entry := provider.Entries()["shared-name"]
	assert.Equal(t, "cr-a", entry.ResourceName)
	assert.Equal(t, "https://a.example.com:6443", entry.Cluster.GetConfig().Host)
	assert.Equal(t, []string{"add:shared-name"}, hook.events)

	// Deleting the losing CR is a no-op; deleting the owner deregisters the entry.
	m.remove(ctx, "cr-b", zap.S())
	assert.Len(t, provider.Entries(), 1)
	m.remove(ctx, "cr-a", zap.S())
	assert.Empty(t, provider.Entries())
}

func TestEnsureConcurrentDuplicateClusterName(t *testing.T) {
	ctx := t.Context()
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		credentialSecret("mck-credential-a", "https://a.example.com:6443"),
		credentialSecret("mck-credential-b", "https://b.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()

	// A barrier in the factory holds both ensures open until each has passed its initial
	// state checks, forcing the race on the committing critical section.
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	factory := func(restConfig *restclient.Config) (cluster.Cluster, error) {
		entered <- struct{}{}
		<-release
		return newTestCluster(restConfig)
	}
	m := newProviderManager(ctx, c, testNamespace, testClientTimeout, provider, factory)

	mcA := memberClusterCR("cr-a", "shared-name", "mck-credential-a")
	credsA := mustLoadCredentials(t, ctx, m, mcA)
	mcB := memberClusterCR("cr-b", "shared-name", "mck-credential-b")
	credsB := mustLoadCredentials(t, ctx, m, mcB)

	errs := make(chan error, 2)
	go func() { errs <- m.ensure(ctx, mcA, credsA, zap.S()) }()
	go func() { errs <- m.ensure(ctx, mcB, credsB, zap.S()) }()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("both ensures did not enter the cluster factory")
		}
	}
	close(release)
	errA, errB := <-errs, <-errs

	succeeded := 0
	for _, err := range []error{errA, errB} {
		if err == nil {
			succeeded++
		} else {
			assert.ErrorContains(t, err, "already registered")
		}
	}
	assert.Equal(t, 1, succeeded, "exactly one of the racing ensures must win")
	assert.Len(t, provider.Entries(), 1)
}

func TestRemoveKeepsProviderEntryOwnedByAnotherCR(t *testing.T) {
	ctx := t.Context()
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		credentialSecret("mck-credential-a", "https://a.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()
	hook := &recordedHook{}
	provider.RegisterHooks(ctx, hook.hooks())
	m := newTestProviderManager(ctx, c, provider)

	mcA := memberClusterCR("cr-a", "shared-name", "mck-credential-a")
	require.NoError(t, m.ensure(ctx, mcA, mustLoadCredentials(t, ctx, m, mcA), zap.S()))

	// Simulate ownership transferred after cr-a's entry was removed from m.entries (a
	// delete/recreate race): the provider entry now belongs to cr-b.
	provider.Set(ctx, "shared-name", multicluster.Entry{ResourceName: "cr-b"})

	m.remove(ctx, "cr-a", zap.S())
	entry, ok := provider.Entries()["shared-name"]
	require.True(t, ok, "the provider entry owned by cr-b must survive cr-a's removal")
	assert.Equal(t, "cr-b", entry.ResourceName)
	assert.Equal(t, []string{"add:shared-name", "add:shared-name"}, hook.events)
}

func TestEnsureRenameKeepsReRegisteredClusterName(t *testing.T) {
	ctx := t.Context()
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		credentialSecret("mck-credential-a", "https://a.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()
	hook := &recordedHook{}
	provider.RegisterHooks(ctx, hook.hooks())
	m := newTestProviderManager(ctx, c, provider)

	mcA := memberClusterCR("cr-a", "cluster-x", "mck-credential-a")
	require.NoError(t, m.ensure(ctx, mcA, mustLoadCredentials(t, ctx, m, mcA), zap.S()))

	// Simulate CR B registering cluster-x after cr-a renamed away: its provider entry
	// must survive cr-a's rebuild.
	provider.Set(ctx, "cluster-x", multicluster.Entry{ResourceName: "cr-b"})

	mcARenamed := withGeneration(memberClusterCR("cr-a", "cluster-y", "mck-credential-a"), 2)
	require.NoError(t, m.ensure(ctx, mcARenamed, mustLoadCredentials(t, ctx, m, mcARenamed), zap.S()))

	entries := provider.Entries()
	require.Len(t, entries, 2)
	assert.Equal(t, "cr-b", entries["cluster-x"].ResourceName)
	assert.Equal(t, "cr-a", entries["cluster-y"].ResourceName)
	assert.Equal(t, []string{"add:cluster-x", "add:cluster-x", "add:cluster-y"}, hook.events)
}

func TestEnsureKeepsOldEntryWhenRebuildFails(t *testing.T) {
	ctx := t.Context()
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		credentialSecret("mck-credential-cluster-east", "https://east.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()

	calls := 0
	failingFactory := func(restConfig *restclient.Config) (cluster.Cluster, error) {
		calls++
		if calls > 1 {
			return nil, fmt.Errorf("bad config")
		}
		return newTestCluster(restConfig)
	}
	m := newProviderManager(ctx, c, testNamespace, testClientTimeout, provider, failingFactory)

	mc := memberClusterCR("cluster-east", "cluster-east", "mck-credential-cluster-east")
	require.NoError(t, m.ensure(ctx, mc, mustLoadCredentials(t, ctx, m, mc), zap.S()))
	require.Len(t, provider.Entries(), 1)

	// The spec change fails to build: the old entry keeps serving.
	mcV2 := withGeneration(memberClusterCR("cluster-east", "cluster-east", "mck-credential-cluster-east"), 2)
	err := m.ensure(ctx, mcV2, mustLoadCredentials(t, ctx, m, mcV2), zap.S())
	require.ErrorContains(t, err, "bad config")
	assert.Equal(t, "https://east.example.com:6443", provider.Entries()["cluster-east"].Cluster.GetConfig().Host)
}

func TestStartDrainsEntriesOnShutdown(t *testing.T) {
	ctx := t.Context()
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		credentialSecret("mck-credential-cluster-east", "https://east.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()

	// A cluster whose Start blocks until its context is cancelled, like a real one.
	blockingFactory := func(restConfig *restclient.Config) (cluster.Cluster, error) {
		base, err := newTestCluster(restConfig)
		return &blockingCluster{Cluster: base}, err
	}
	m := newProviderManager(ctx, c, testNamespace, testClientTimeout, provider, blockingFactory)
	mc := memberClusterCR("cluster-east", "cluster-east", "mck-credential-cluster-east")
	require.NoError(t, m.ensure(ctx, mc, mustLoadCredentials(t, ctx, m, mc), zap.S()))

	mgrCtx, mgrCancel := context.WithCancel(ctx)
	startDone := make(chan error, 1)
	go func() { startDone <- m.Start(mgrCtx) }()

	select {
	case <-startDone:
		t.Fatal("Start returned before the manager context was cancelled")
	case <-time.After(100 * time.Millisecond):
	}

	mgrCancel()
	select {
	case err := <-startDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after shutdown: per-entry goroutines were not drained")
	}
}

type blockingCluster struct {
	cluster.Cluster
}

func (c *blockingCluster) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func TestEnsureRefusesWorkAfterShutdown(t *testing.T) {
	ctx := t.Context()
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		credentialSecret("mck-credential-cluster-east", "https://east.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()

	mgrCtx, mgrCancel := context.WithCancel(ctx)
	mgrCancel()
	m := newProviderManager(mgrCtx, c, testNamespace, testClientTimeout, provider, newTestCluster)
	require.NoError(t, m.Start(mgrCtx))

	mc := memberClusterCR("cluster-east", "cluster-east", "mck-credential-cluster-east")
	require.NoError(t, m.ensure(ctx, mc, mustLoadCredentials(t, ctx, m, mc), zap.S()))
	assert.Empty(t, provider.Entries())
	assert.Empty(t, m.entries)
}

func TestStartDrainWinsAgainstInFlightEnsure(t *testing.T) {
	ctx := t.Context()
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithObjects(
		credentialSecret("mck-credential-cluster-east", "https://east.example.com:6443"),
	).Build()
	provider := multicluster.NewProvider()

	// A factory that blocks inside the build, holding ensure open while the drain starts.
	entered := make(chan struct{})
	release := make(chan struct{})
	factory := func(restConfig *restclient.Config) (cluster.Cluster, error) {
		close(entered)
		<-release
		return newTestCluster(restConfig)
	}

	mgrCtx, mgrCancel := context.WithCancel(ctx)
	m := newProviderManager(mgrCtx, c, testNamespace, testClientTimeout, provider, factory)
	mc := memberClusterCR("cluster-east", "cluster-east", "mck-credential-cluster-east")
	creds := mustLoadCredentials(t, ctx, m, mc)

	ensureDone := make(chan error, 1)
	go func() { ensureDone <- m.ensure(ctx, mc, creds, zap.S()) }()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ensure did not enter the cluster factory")
	}

	mgrCancel()
	startDone := make(chan error, 1)
	go func() { startDone <- m.Start(mgrCtx) }()
	select {
	case err := <-startDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after shutdown")
	}
	close(release)

	select {
	case err := <-ensureDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ensure did not return after the factory was released")
	}
	assert.Empty(t, provider.Entries())
	assert.Empty(t, m.entries)
}
