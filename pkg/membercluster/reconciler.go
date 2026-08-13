// Package membercluster contains the operator-side wiring that keeps the member-cluster
// registry in sync with the MemberCluster CRs and their per-cluster credential Secrets.
package membercluster

import (
	"context"
	"errors"
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
	"github.com/mongodb/mongodb-kubernetes/pkg/resourcenames"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// Reconciler is the controller for MemberCluster CRs. For every CR it validates the member
// cluster's RBAC (by probing the operator's ServiceAccount there), reports the outcome as
// the RBACValid status condition, and drives the provider-entry lifecycle via
// providerManager: a valid (or transiently unprobeable) cluster gets a runtime entry, a
// definitively invalid one is deregistered so workload reconcilers skip it. Every
// reconciliation requeues after recheckInterval, so RBAC fixes and credential rotation are
// picked up without waiting for a CR event. The initial informer replay registers the CRs
// that already exist, so no startup discovery is needed.
type Reconciler struct {
	client          client.Client
	providerMgr     *providerManager
	recheckInterval time.Duration
	validator       rbacValidator
	expectedVersion string
}

func NewReconciler(baseCtx context.Context, c client.Client, namespace string, clientTimeout time.Duration, recheckInterval time.Duration, provider *multicluster.Provider, newCluster func(restConfig *restclient.Config) (cluster.Cluster, error)) *Reconciler {
	return &Reconciler{
		client:          c,
		providerMgr:     newProviderManager(baseCtx, c, namespace, clientTimeout, provider, newCluster),
		recheckInterval: recheckInterval,
		validator:       newRBACValidator(),
		expectedVersion: util.ExpectedMemberRBACVersion,
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

	creds, err := r.providerMgr.loadCredentials(ctx, mc)
	if err != nil {
		reason := reasonCredentialSecretMissing
		switch {
		case errors.Is(err, errCredentialMalformed):
			reason = reasonCredentialInvalid
		case errors.Is(err, errCredentialNamespaceMissing):
			reason = reasonCredentialNamespaceMissing
		}
		if err := r.setRBACValidCondition(ctx, mc, original, log, metav1.ConditionFalse, reason, err.Error()); err != nil {
			return ctrl.Result{}, err
		}
		r.providerMgr.remove(ctx, mc.Name, log)
		// Expected-negative path: no error return, so no backoff storm; the periodic
		// re-check picks the credentials up once fixed.
		return ctrl.Result{RequeueAfter: r.recheckInterval}, nil
	}

	var outcome probeOutcome
	if r.expectedVersion == "" {
		outcome = probeOutcome{
			metav1.ConditionTrue, reasonValidationDisabled,
			"The operator was built without an expected RBAC version; RBAC validation is disabled.",
		}
	} else {
		outcome = r.validator.Probe(ctx, creds.restConfig, resourcenames.MemberClusterServiceAccountName(mc.Name), creds.saNamespace, r.expectedVersion)
	}

	if outcome.status == metav1.ConditionFalse {
		r.providerMgr.remove(ctx, mc.Name, log)
	} else if err := r.providerMgr.ensure(ctx, mc, creds, log); err != nil {
		// Backoff is appropriate for cluster-build failures.
		return ctrl.Result{}, err
	}

	if err := r.setRBACValidCondition(ctx, mc, original, log, outcome.status, outcome.reason, outcome.message); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.recheckInterval}, nil
}

// setRBACValidCondition writes the RBACValid condition when it changed and logs the
// transition; unchanged conditions are left untouched, so steady-state reconciles issue
// no API writes.
func (r *Reconciler) setRBACValidCondition(ctx context.Context, mc *operatorv1.MemberCluster, original *operatorv1.MemberCluster, log *zap.SugaredLogger, status metav1.ConditionStatus, reason, message string) error {
	changed := apimeta.SetStatusCondition(&mc.Status.Conditions, metav1.Condition{
		Type:               operatorv1.MemberClusterConditionRBACValid,
		Status:             status,
		ObservedGeneration: mc.Generation,
		Reason:             reason,
		Message:            message,
	})
	if !changed {
		return nil
	}
	log.Infof("Member cluster %q RBACValid condition set to %s (%s): %s", mc.Spec.ClusterName, status, reason, message)
	return r.client.Status().Patch(ctx, mc, client.MergeFrom(original))
}
