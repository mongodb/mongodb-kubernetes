// Package membercluster contains the operator-side wiring that keeps the member-cluster
// registry in sync with the MemberCluster CRs and their per-cluster credential Secrets.
package membercluster

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/api/equality"
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

// Reconciler is the controller for MemberCluster CRs. For every CR it loads the member
// cluster's credentials via providerManager, validates the cluster's RBAC, and drives the
// provider entry accordingly: a valid (or transiently unprobeable) cluster gets a runtime
// entry, a definitively invalid one is deregistered so workload reconcilers skip it. The
// outcome is recorded as the RBACValid status condition, and every reconciliation requeues
// after recheckInterval so RBAC fixes and credential rotation are picked up.
type Reconciler struct {
	client          client.Client
	providerMgr     *providerManager
	validator       rbacValidator
	recheckInterval time.Duration
}

func NewReconciler(baseCtx context.Context, c client.Client, namespace string, clientTimeout time.Duration, recheckInterval time.Duration, provider *multicluster.Provider, newCluster func(restConfig *restclient.Config) (cluster.Cluster, error)) *Reconciler {
	return &Reconciler{
		client:          c,
		providerMgr:     newProviderManager(baseCtx, c, namespace, clientTimeout, provider, newCluster),
		validator:       newRBACValidator(),
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
			// The CR was deleted, so we need to remove the provider entry.
			r.providerMgr.remove(ctx, req.Name, log)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	original := mc.DeepCopy()

	outcome, err := r.validateAndDriveProvider(ctx, mc, log)
	if err != nil {
		return ctrl.Result{}, err
	}

	apimeta.SetStatusCondition(&mc.Status.Conditions, metav1.Condition{
		Type:               operatorv1.MemberClusterConditionRBACValid,
		Status:             outcome.status,
		ObservedGeneration: mc.Generation,
		Reason:             outcome.reason,
		Message:            outcome.message,
	})
	if equality.Semantic.DeepEqual(original.Status, mc.Status) {
		return ctrl.Result{RequeueAfter: r.recheckInterval}, nil
	}
	log.Infof("Member cluster %q RBACValid condition set to %s (%s): %s", mc.Spec.ClusterName, outcome.status, outcome.reason, outcome.message)
	if err := r.client.Status().Update(ctx, mc); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %v", err)
	}
	return ctrl.Result{RequeueAfter: r.recheckInterval}, nil
}

func (r *Reconciler) validateAndDriveProvider(ctx context.Context, mc *operatorv1.MemberCluster, log *zap.SugaredLogger) (probeOutcome, error) {
	creds, err := r.providerMgr.loadCredentials(ctx, mc)

	var outcome probeOutcome
	if err != nil {
		outcome = probeOutcome{metav1.ConditionFalse, reasonInvalid, err.Error()}
	} else {
		outcome = r.validator.validate(ctx, mc, creds.restConfig, creds.saNamespace)
	}
	if outcome.status == metav1.ConditionFalse {
		// When RBAC is not valid, remove the provider entry so workload reconcilers skip this cluster.
		r.providerMgr.remove(ctx, mc.Name, log)
	} else {
		// Unknown means the probe got no definitive answer (e.g. cluster unreachable or
		// timed out), so the entry must stay: its informers and the memberwatch health
		// checker keep running, and a genuine network partition still produces
		// failed-cluster annotations (and auto-failover when enabled). Only a definitive
		// False gates a cluster out of the provider.
		if err := r.providerMgr.ensure(ctx, mc, creds, log); err != nil {
			return probeOutcome{}, err
		}
	}
	return outcome, nil
}
