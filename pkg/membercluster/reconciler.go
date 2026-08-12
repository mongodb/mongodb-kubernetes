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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	restclient "k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
)

// credentialSecretKubeconfigKey is the Secret key holding the single-context kubeconfig.
// It matches the key written by the `generate-member-registration` plugin command.
const credentialSecretKubeconfigKey = "kubeconfig"

// Reconciler keeps the operator's member-cluster registry in sync with the MemberCluster
// CRs: every CR add or spec change (re)builds the cluster's runtime entry — rest.Config,
// cluster.Cluster started with a per-entry context, provider registration, engage hooks —
// and every CR delete removes the entry and stops its informers. The initial informer
// replay populates the registry, so no startup discovery is needed. Membership changes are
// picked up without restarting the operator.
//
// The reconciler writes no status on the MemberCluster CR.
type Reconciler struct {
	client        client.Client
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
	// logical cluster name, its context cancel func, and the reconciled generation, so spec
	// changes rebuild the entry while status writes and resyncs leave it untouched.
	entries map[string]entryState
}

type entryState struct {
	clusterName string
	cancel      context.CancelFunc
	generation  int64
}

func NewReconciler(c client.Client, namespace string, clientTimeout time.Duration, provider *multicluster.Provider, newCluster func(restConfig *restclient.Config) (cluster.Cluster, error), baseCtx context.Context) *Reconciler {
	return &Reconciler{
		client:        c,
		namespace:     namespace,
		clientTimeout: clientTimeout,
		provider:      provider,
		newCluster:    newCluster,
		baseCtx:       baseCtx,
		entries:       map[string]entryState{},
	}
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("membercluster").
		For(&operatorv1.MemberCluster{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zap.S().With("membercluster", req.NamespacedName)

	mc := &operatorv1.MemberCluster{}
	if err := r.client.Get(ctx, req.NamespacedName, mc); err != nil {
		if apierrors.IsNotFound(err) {
			r.removeEntry(ctx, req.Name, log)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	r.mu.Lock()
	state, exists := r.entries[req.Name]
	r.mu.Unlock()
	if exists && state.generation == mc.Generation {
		return ctrl.Result{}, nil
	}

	restConfig, err := restConfigFromMemberCluster(ctx, r.client, mc, r.namespace)
	if err != nil {
		// Requeue with backoff: the credential Secret may not exist yet.
		return ctrl.Result{}, err
	}
	restConfig.Timeout = r.clientTimeout

	if exists {
		r.removeEntry(ctx, req.Name, log)
	}

	memberCluster, err := r.newCluster(restConfig)
	if err != nil {
		return ctrl.Result{}, err
	}

	entryCtx, cancel := context.WithCancel(r.baseCtx)
	go func() {
		if err := memberCluster.Start(entryCtx); err != nil {
			log.Errorf("Member cluster %q stopped with error: %s", mc.Spec.ClusterName, err)
		}
	}()

	r.mu.Lock()
	r.entries[req.Name] = entryState{clusterName: mc.Spec.ClusterName, cancel: cancel, generation: mc.Generation}
	r.mu.Unlock()

	r.provider.Set(ctx, mc.Spec.ClusterName, multicluster.Entry{
		Cluster:      memberCluster,
		Client:       memberCluster.GetClient(),
		ResourceName: mc.Name,
	})
	log.Infof("Member cluster %q registered", mc.Spec.ClusterName)
	return ctrl.Result{}, nil
}

// removeEntry deregisters the entry built from the MemberCluster CR named resourceName (if
// any) and cancels its context, stopping the cluster's informers.
func (r *Reconciler) removeEntry(ctx context.Context, resourceName string, log *zap.SugaredLogger) {
	r.mu.Lock()
	state, exists := r.entries[resourceName]
	if exists {
		delete(r.entries, resourceName)
	}
	r.mu.Unlock()

	if !exists {
		return
	}
	r.provider.Delete(ctx, state.clusterName)
	state.cancel()
	log.Infof("Member cluster %q deregistered", state.clusterName)
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
