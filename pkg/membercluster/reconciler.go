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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	restclient "k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
)

// Reconciler is the controller for MemberCluster CRs. It delegates each CR to
// rbacValidation, which validates the member cluster's RBAC and drives the provider-entry
// lifecycle — a valid (or transiently unprobeable) cluster gets a runtime entry, a
// definitively invalid one is deregistered so workload reconcilers skip it — and then
// applies the resulting RBACValid status condition in a single status write at the end of
// the reconciliation. Every reconciliation requeues after recheckInterval, so RBAC fixes
// and credential rotation are picked up without waiting for a CR event. The initial
// informer replay registers the CRs that already exist, so no startup discovery is needed.
type Reconciler struct {
	client          client.Client
	providerMgr     *providerManager
	validation      *rbacValidation
	recheckInterval time.Duration
}

func NewReconciler(baseCtx context.Context, c client.Client, namespace string, clientTimeout time.Duration, recheckInterval time.Duration, provider *multicluster.Provider, newCluster func(restConfig *restclient.Config) (cluster.Cluster, error)) *Reconciler {
	providerMgr := newProviderManager(baseCtx, c, namespace, clientTimeout, provider, newCluster)
	return &Reconciler{
		client:          c,
		providerMgr:     providerMgr,
		validation:      newRBACValidation(providerMgr),
		recheckInterval: recheckInterval,
	}
}

// SetupWithManager registers the controller. The generation filter keeps our own status
// writes (which never bump the CR's generation) from triggering pointless re-probes;
// create and delete events still pass through it.
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
			r.providerMgr.remove(ctx, req.Name, log)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	original := mc.DeepCopy()

	// Expected-negative validation outcomes come back as conditions; only unexpected
	// failures are errors and requeue with backoff.
	rbacValid, err := r.validation.validate(ctx, mc, log)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Status is written exactly once per reconciliation, after every step has run.
	if err := r.setConditions(ctx, mc, original, log, rbacValid); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.recheckInterval}, nil
}

// setConditions applies the given RBACValid outcomes to the CR's status and writes them
// with a single status patch when anything changed, logging each transition; unchanged
// conditions are left untouched, so steady-state reconciles issue no API writes.
func (r *Reconciler) setConditions(ctx context.Context, mc *operatorv1.MemberCluster, original *operatorv1.MemberCluster, log *zap.SugaredLogger, outcomes ...probeOutcome) error {
	changed := false
	for _, outcome := range outcomes {
		if apimeta.SetStatusCondition(&mc.Status.Conditions, metav1.Condition{
			Type:               operatorv1.MemberClusterConditionRBACValid,
			Status:             outcome.status,
			ObservedGeneration: mc.Generation,
			Reason:             outcome.reason,
			Message:            outcome.message,
		}) {
			changed = true
			log.Infof("Member cluster %q RBACValid condition set to %s (%s): %s", mc.Spec.ClusterName, outcome.status, outcome.reason, outcome.message)
		}
	}
	if !changed {
		return nil
	}
	return r.client.Status().Patch(ctx, mc, client.MergeFrom(original))
}
