// Package membercluster contains the operator-side wiring that keeps the member-cluster
// registry in sync with the MemberCluster CRs and their per-cluster credential Secrets.
package membercluster

import (
	"context"
	"time"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	restclient "k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
)

// Reconciler is the controller for MemberCluster CRs. For every CR it keeps the provider
// entry in sync with the CR and its credential Secret: the entry is (re)built when the CR
// generation or the credential kubeconfig hash changes and removed when the CR is deleted.
// Credential rotation and late Secret creation are picked up via the Secret watch; there
// is no periodic requeue.
type Reconciler struct {
	client      client.Client
	namespace   string
	providerMgr *providerManager
}

func NewReconciler(baseCtx context.Context, c client.Client, namespace string, clientTimeout time.Duration, provider *multicluster.Provider, newCluster func(restConfig *restclient.Config) (cluster.Cluster, error)) *Reconciler {
	return &Reconciler{
		client:      c,
		namespace:   namespace,
		providerMgr: newProviderManager(baseCtx, c, clientTimeout, provider, newCluster),
	}
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	// providerManager.Start drains the per-entry cluster goroutines on manager shutdown.
	if err := mgr.Add(r.providerMgr); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("membercluster").
		For(&operatorv1.MemberCluster{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.mapCredentialSecretToMemberClusters)).
		Complete(r)
}

// mapCredentialSecretToMemberClusters enqueues every MemberCluster CR that references
// the given Secret as its credential, so credential rotation and late Secret creation
// reconcile the affected clusters.
func (r *Reconciler) mapCredentialSecretToMemberClusters(ctx context.Context, obj client.Object) []reconcile.Request {
	// A credential Secret is read from the namespace of the MemberCluster CR that
	// references it, and MemberCluster CRs are only listed from the operator namespace.
	if obj.GetNamespace() != r.namespace {
		return nil
	}
	var list operatorv1.MemberClusterList
	if err := r.client.List(ctx, &list, client.InNamespace(r.namespace)); err != nil {
		zap.S().Errorf("failed to list MemberCluster resources in %s: %v", r.namespace, err)
		return nil
	}
	var requests []reconcile.Request
	for _, mc := range list.Items {
		if mc.Spec.CredentialSecretRef.Name == obj.GetName() {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: mc.Name, Namespace: mc.Namespace},
			})
		}
	}
	return requests
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zap.S().With("membercluster", req.NamespacedName)

	mc := &operatorv1.MemberCluster{}
	if err := r.client.Get(ctx, req.NamespacedName, mc); err != nil {
		if apierrors.IsNotFound(err) {
			// The CR was deleted, so we need to remove the provider entry.
			r.providerMgr.remove(ctx, req.Name, log)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if err := r.reconcile(ctx, mc, log); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) reconcile(ctx context.Context, mc *operatorv1.MemberCluster, log *zap.SugaredLogger) error {
	creds, err := r.providerMgr.loadCredentials(ctx, mc)
	if err != nil {
		// The entry cannot be (re)built without usable credentials; an entry from an
		// earlier successful reconcile keeps running. The Secret read goes through the
		// cached client, and the Secret watch re-triggers the reconcile on any fix.
		log.Warnf("Member cluster %q credentials unusable: %v", mc.Spec.ClusterName, err)
		return nil
	}
	// TODO(m1kola): surface a duplicate clusterName refusal on the CR's status via a
	// dedicated condition (e.g. Accepted) instead of only erroring here.
	return r.providerMgr.ensure(ctx, mc, creds, log)
}
