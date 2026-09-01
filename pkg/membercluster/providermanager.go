package membercluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// credentials holds everything derived from the MemberCluster CR's credential Secret.
type credentials struct {
	restConfig *restclient.Config
	// kubeconfigHash detects credential rotation: a changed hash with an unchanged CR
	// generation still rebuilds the entry.
	kubeconfigHash string
}

// providerManager owns the lifecycle of the operator's member-cluster provider entries,
// driven by the Reconciler.
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
	// logical cluster name, its context cancel func, and the synced generation and
	// credential hash, so spec changes and credential rotation rebuild the entry while
	// status writes and resyncs leave it untouched.
	entries map[string]entryState
	// wg tracks the running per-entry cluster goroutines so Start can drain them on
	// manager shutdown.
	wg sync.WaitGroup
	// stopped is set by Start before draining; ensure refuses new work once set.
	stopped bool
}

type entryState struct {
	clusterName    string
	cancel         context.CancelFunc
	generation     int64
	kubeconfigHash string
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

// loadCredentials reads the credential Secret referenced by the MemberCluster CR and
// derives the member cluster's rest.Config and a hash of the raw kubeconfig for
// rotation detection.
func (m *providerManager) loadCredentials(ctx context.Context, mc *operatorv1.MemberCluster) (*credentials, error) {
	secretName := mc.Spec.CredentialSecretRef.Name
	secret := &corev1.Secret{}
	if err := m.client.Get(ctx, types.NamespacedName{Name: secretName, Namespace: m.namespace}, secret); err != nil {
		return nil, fmt.Errorf("reading credential secret %q: %w", secretName, err)
	}

	kubeconfig, ok := secret.Data[util.MemberClusterCredentialSecretKubeconfigKey]
	if !ok || len(kubeconfig) == 0 {
		return nil, fmt.Errorf("credential secret %q has no %q key", secretName, util.MemberClusterCredentialSecretKubeconfigKey)
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("building REST config from credential secret %q: %w", secretName, err)
	}
	restConfig.Timeout = m.clientTimeout

	sum := sha256.Sum256(kubeconfig)
	return &credentials{
		restConfig:     restConfig,
		kubeconfigHash: hex.EncodeToString(sum[:]),
	}, nil
}

// ensure registers the provider entry for mc, rebuilding it when the CR's generation or
// the credential hash changed. A resync or a status-only update (both leaving generation
// and hash unchanged) is a no-op.
func (m *providerManager) ensure(ctx context.Context, mc *operatorv1.MemberCluster, creds *credentials, log *zap.SugaredLogger) error {
	// Build the replacement before touching any state: construction is local (the
	// RESTMapper is lazy and the cache starts only at Start), and if it fails the old
	// entry keeps serving.
	memberCluster, err := m.newCluster(creds.restConfig)
	if err != nil {
		return err
	}

	m.mu.Lock()
	if m.stopped {
		// We are shutting down, so no need to start a new cluster.
		m.mu.Unlock()
		return nil
	}
	state, exists := m.entries[mc.Name]
	if exists && state.generation == mc.Generation && state.kubeconfigHash == creds.kubeconfigHash {
		m.mu.Unlock()
		return nil
	}
	// spec.clusterName is unique per member cluster by contract, but nothing at the API
	// level can enforce cross-object uniqueness. Refuse a second registration at runtime:
	// first writer wins, and the conflicting CR's reconcile keeps failing loudly.
	for resourceName, other := range m.entries {
		if resourceName != mc.Name && other.clusterName == mc.Spec.ClusterName {
			m.mu.Unlock()
			return fmt.Errorf("clusterName %q is already registered by MemberCluster %q", mc.Spec.ClusterName, resourceName)
		}
	}
	if exists {
		// Cancelling is cheap and non-blocking, so it is safe under the lock.
		state.cancel()
	}
	entryCtx, cancel := context.WithCancel(m.baseCtx)
	m.entries[mc.Name] = entryState{clusterName: mc.Spec.ClusterName, cancel: cancel, generation: mc.Generation, kubeconfigHash: creds.kubeconfigHash}
	// wg.Go stays inside the critical section: its Add then happens-before the wg.Wait in
	// Start's drain, which a WaitGroup Add racing a Wait at counter zero would panic.
	m.wg.Go(func() {
		if err := memberCluster.Start(entryCtx); err != nil {
			log.Errorf("Member cluster %q stopped with error: %s", mc.Spec.ClusterName, err)
		}
	})
	m.mu.Unlock()

	// Provider hooks (OnAdd/OnRemove) may block on channel sends, so they must fire
	// outside the lock.
	if exists {
		// The old clusterName may have been re-registered by another CR since the commit;
		// Delete's ownership comparison is atomic with the delete.
		m.provider.Delete(ctx, state.clusterName, mc.Name)
	}
	m.provider.Set(ctx, mc.Spec.ClusterName, multicluster.Entry{
		Cluster:      memberCluster,
		Client:       memberCluster.GetClient(),
		ResourceName: mc.Name,
	})
	log.Infof("Member cluster %q registered", mc.Spec.ClusterName)
	return nil
}

// remove deregisters the entry built from the MemberCluster CR named resourceName (if any)
// and cancels its context, stopping the cluster's informers. The clusterName may have been
// re-registered by another CR since the entry was removed under mu; Delete's ownership
// comparison is atomic with the delete.
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
	m.provider.Delete(ctx, state.clusterName, resourceName)
	state.cancel()
	log.Infof("Member cluster %q deregistered", state.clusterName)
}

// Start implements manager.Runnable: it blocks until the manager stops, then stops every
// live entry and waits for the per-entry cluster goroutines to drain, so the manager's
// graceful shutdown covers member-cluster teardown. stopped is set under the lock before
// draining, so an ensure still building its cluster cannot register or start a goroutine
// once the drain begins.
func (m *providerManager) Start(ctx context.Context) error {
	<-ctx.Done()

	m.mu.Lock()
	m.stopped = true
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
