// Package membercluster contains the operator-side wiring that keeps the member-cluster
// registry in sync with the MemberCluster CRs and their per-cluster credential Secrets.
package membercluster

import (
	"context"
	"time"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	restclient "k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
)

// Reconciler is the controller for MemberCluster CRs. For every CR it keeps the provider
// entry in sync with the CR and its credential Secret: the entry is (re)built when the CR
// generation or the credential kubeconfig hash changes and removed when the CR is deleted.
// Every reconciliation requeues after recheckInterval so credential rotation and late
// Secret creation are picked up.
type Reconciler struct {
	client          client.Client
	providerMgr     *providerManager
	recheckInterval time.Duration
}

func NewReconciler(baseCtx context.Context, c client.Client, namespace string, clientTimeout time.Duration, recheckInterval time.Duration, provider *multicluster.Provider, newCluster func(restConfig *restclient.Config) (cluster.Cluster, error)) *Reconciler {
	return &Reconciler{
		client:          c,
		providerMgr:     newProviderManager(baseCtx, c, namespace, clientTimeout, provider, newCluster),
		recheckInterval: recheckInterval,
	}
}

// SetupWithManager registers the controller. The generation filter skips resyncs and
// updates that leave the spec untouched; create, delete and spec-change events pass
// through it, and the periodic requeue covers what no event can (credential rotation).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	// providerManager.Start drains the per-entry cluster goroutines on manager shutdown.
	if err := mgr.Add(r.providerMgr); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("membercluster").
		For(&operatorv1.MemberCluster{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
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
	return ctrl.Result{RequeueAfter: r.recheckInterval}, nil
}

func (r *Reconciler) reconcile(ctx context.Context, mc *operatorv1.MemberCluster, log *zap.SugaredLogger) error {
	creds, err := r.providerMgr.loadCredentials(ctx, mc)
	if err != nil {
		// The entry cannot be (re)built without usable credentials; an entry from an
		// earlier successful reconcile keeps running. The periodic requeue retries.
		log.Warnf("Member cluster %q credentials unusable: %v", mc.Spec.ClusterName, err)
		return nil
	}
	// TODO(m1kola): surface a duplicate clusterName refusal on the CR's status via a
	// dedicated condition (e.g. Accepted) instead of only erroring here.
	return r.providerMgr.ensure(ctx, mc, creds, log)
}
