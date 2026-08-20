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
	// saNamespace is the kubeconfig context's namespace: the member cluster's operator
	// namespace, where the operator's ServiceAccount lives.
	saNamespace string
	// kubeconfigHash detects credential rotation: a changed hash with an unchanged CR
	// generation still rebuilds the entry.
	kubeconfigHash string
}

// providerManager owns the lifecycle of the operator's member-cluster provider entries,
// driven by the Reconciler. loadCredentials derives the member cluster's rest.Config from
// the CR's credential Secret, ensure (re)builds the entry — cluster.Cluster started with a
// per-entry context, provider registration, engage hooks — and remove tears it down,
// cancelling the entry's context so its informers stop. Membership changes are applied
// without restarting the operator.
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
// derives the member cluster's rest.Config, the ServiceAccount namespace from the
// kubeconfig's context, and a hash of the raw kubeconfig for rotation detection.
func (m *providerManager) loadCredentials(ctx context.Context, mc *operatorv1.MemberCluster) (*credentials, error) {
	secretName := mc.Spec.CredentialSecretRef.Name
	secret := &corev1.Secret{}
	if err := m.client.Get(ctx, types.NamespacedName{Name: secretName, Namespace: m.namespace}, secret); err != nil {
		return nil, fmt.Errorf("reading credential secret %q: %v", secretName, err)
	}

	kubeconfig, ok := secret.Data[util.MemberClusterCredentialSecretKubeconfigKey]
	if !ok || len(kubeconfig) == 0 {
		return nil, fmt.Errorf("credential secret %q has no %q key", secretName, util.MemberClusterCredentialSecretKubeconfigKey)
	}

	cfg, err := clientcmd.Load(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("parsing kubeconfig in credential secret %q: %v", secretName, err)
	}
	restConfig, err := clientcmd.NewDefaultClientConfig(*cfg, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("building REST config from credential secret %q: %v", secretName, err)
	}
	restConfig.Timeout = m.clientTimeout

	contextInfo := cfg.Contexts[cfg.CurrentContext]
	if contextInfo == nil || contextInfo.Namespace == "" {
		return nil, fmt.Errorf("kubeconfig in credential secret %q has no namespace on context %q: set contexts[].context.namespace to the member cluster's operator namespace ('kubectl mongodb multicluster generate-member-registration' sets it automatically)", secretName, cfg.CurrentContext)
	}

	sum := sha256.Sum256(kubeconfig)
	return &credentials{
		restConfig:     restConfig,
		saNamespace:    contextInfo.Namespace,
		kubeconfigHash: hex.EncodeToString(sum[:]),
	}, nil
}

// ensure registers the provider entry for mc, rebuilding it when the CR's generation or
// the credential hash changed. A resync or a status-only update (both leaving generation
// and hash unchanged) is a no-op.
func (m *providerManager) ensure(ctx context.Context, mc *operatorv1.MemberCluster, creds *credentials, log *zap.SugaredLogger) error {
	m.mu.Lock()
	state, exists := m.entries[mc.Name]
	m.mu.Unlock()
	if exists && state.generation == mc.Generation && state.kubeconfigHash == creds.kubeconfigHash {
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

	// Build the replacement before tearing the old entry down: if construction fails, the
	// old entry keeps serving.
	memberCluster, err := m.newCluster(creds.restConfig)
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
	m.entries[mc.Name] = entryState{clusterName: mc.Spec.ClusterName, cancel: cancel, generation: mc.Generation, kubeconfigHash: creds.kubeconfigHash}
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
