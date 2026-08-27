package membercluster

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	corev1 "k8s.io/api/core/v1"
	restclient "k8s.io/client-go/rest"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
)

// credentialSecretKubeconfigKey is the Secret key holding the single-context kubeconfig.
// It matches the key written by the `generate-member-registration` plugin command.
const credentialSecretKubeconfigKey = "kubeconfig"

// providerManager owns the lifecycle of the operator's member-cluster provider entries,
// driven by the Reconciler. sync (re)builds the entry for a MemberCluster CR — rest.Config,
// cluster.Cluster started with a per-entry context, provider registration, engage hooks —
// and remove tears it down, cancelling the entry's context so its informers stop.
// Membership changes are applied without restarting the operator.
type providerManager struct {
	client        client.Reader
	namespace     string
	clientTimeout time.Duration
	provider      *multicluster.Provider
	// newCluster builds a cluster.Cluster for a member cluster's rest.Config, applying the
	// same options (scheme, watched namespaces) as any other member cluster.
	newCluster func(restConfig *restclient.Config) (cluster.Cluster, error)
	// baseCtx scopes the per-entry contexts: cancelling it (manager shutdown) stops every
	// member cluster's informers.
	baseCtx context.Context

	mu sync.Mutex
	// entries tracks the live entry per MemberCluster CR (keyed by metadata.name): its
	// logical cluster name, its context cancel func, and the synced generation, so spec
	// changes rebuild the entry while status writes and resyncs leave it untouched.
	entries map[string]entryState
	// wg tracks the running per-entry cluster goroutines so Start can drain them on
	// manager shutdown.
	wg sync.WaitGroup
}

type entryState struct {
	clusterName string
	cancel      context.CancelFunc
	generation  int64
}

func newProviderManager(baseCtx context.Context, c client.Reader, namespace string, clientTimeout time.Duration, provider *multicluster.Provider, newCluster func(restConfig *restclient.Config) (cluster.Cluster, error)) *providerManager {
	return &providerManager{
		client:        c,
		namespace:     namespace,
		clientTimeout: clientTimeout,
		provider:      provider,
		newCluster:    newCluster,
		baseCtx:       baseCtx,
		entries:       map[string]entryState{},
	}
}

// sync registers the provider entry for mc, rebuilding it when the CR's generation changed.
// A resync or a status-only update (which leaves the generation unchanged) is a no-op.
func (m *providerManager) sync(ctx context.Context, mc *operatorv1.MemberCluster, log *zap.SugaredLogger) error {
	m.mu.Lock()
	state, exists := m.entries[mc.Name]
	m.mu.Unlock()
	if exists && state.generation == mc.Generation {
		return nil
	}

	// spec.clusterName is unique per member cluster by contract, but nothing at the API
	// level can enforce cross-object uniqueness. Refuse a second registration at runtime:
	// first writer wins, and the conflicting CR's reconcile keeps failing loudly.
	m.mu.Lock()
	for resourceName, state := range m.entries {
		if resourceName != mc.Name && state.clusterName == mc.Spec.ClusterName {
			m.mu.Unlock()
			return fmt.Errorf("clusterName %q is already registered by MemberCluster %q", mc.Spec.ClusterName, resourceName)
		}
	}
	m.mu.Unlock()

	restConfig, err := restConfigFromMemberCluster(ctx, m.client, mc, m.namespace)
	if err != nil {
		// Requeue with backoff: the credential Secret may not exist yet.
		return err
	}
	restConfig.Timeout = m.clientTimeout

	// Build the replacement before tearing the old entry down: if construction fails, the
	// old entry keeps serving.
	memberCluster, err := m.newCluster(restConfig)
	if err != nil {
		return err
	}

	if exists {
		m.remove(ctx, mc.Name, log)
	}

	entryCtx, cancel := context.WithCancel(m.baseCtx)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		if err := memberCluster.Start(entryCtx); err != nil {
			log.Errorf("Member cluster %q stopped with error: %s", mc.Spec.ClusterName, err)
		}
	}()

	m.mu.Lock()
	m.entries[mc.Name] = entryState{clusterName: mc.Spec.ClusterName, cancel: cancel, generation: mc.Generation}
	m.mu.Unlock()

	m.provider.Set(ctx, mc.Spec.ClusterName, multicluster.Entry{
		Cluster:      memberCluster,
		Client:       memberCluster.GetClient(),
		ResourceName: mc.Name,
	})
	log.Infof("Member cluster %q registered", mc.Spec.ClusterName)
	return nil
}

// remove deregisters the entry built from the MemberCluster CR named resourceName (if any)
// and cancels its context, stopping the cluster's informers. The provider entry is removed
// only if it is still owned by this CR.
func (m *providerManager) remove(ctx context.Context, resourceName string, log *zap.SugaredLogger) {
	m.mu.Lock()
	state, exists := m.entries[resourceName]
	if exists {
		delete(m.entries, resourceName)
	}
	m.mu.Unlock()

	if !exists {
		return
	}
	if entry, ok := m.provider.Entries()[state.clusterName]; ok && entry.ResourceName == resourceName {
		m.provider.Delete(ctx, state.clusterName)
	}
	state.cancel()
	log.Infof("Member cluster %q deregistered", state.clusterName)
}

// Start implements manager.Runnable: it blocks until the manager stops, then stops every
// live entry and waits for the per-entry cluster goroutines to drain, so the manager's
// graceful shutdown covers member-cluster teardown.
func (m *providerManager) Start(ctx context.Context) error {
	<-ctx.Done()

	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.entries))
	for _, state := range m.entries {
		cancels = append(cancels, state.cancel)
	}
	m.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	m.wg.Wait()
	return nil
}

// restConfigFromMemberCluster reads the credential Secret referenced by the MemberCluster CR
// and builds a REST config from its single-context kubeconfig.
func restConfigFromMemberCluster(ctx context.Context, c client.Reader, mc *operatorv1.MemberCluster, namespace string) (*restclient.Config, error) {
	secretName := mc.Spec.CredentialSecretRef.Name
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err != nil {
		return nil, fmt.Errorf("reading credential secret %q: %w", secretName, err)
	}

	kubeconfig, ok := secret.Data[credentialSecretKubeconfigKey]
	if !ok || len(kubeconfig) == 0 {
		return nil, fmt.Errorf("credential secret %q has no %q key", secretName, credentialSecretKubeconfigKey)
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("building REST config from credential secret %q: %w", secretName, err)
	}

	return restConfig, nil
}
