package operatorconfig

import (
	"context"
	"sync"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
)

// +kubebuilder:rbac:groups=operator.mongodb.com,resources=operatorconfigs,verbs=get;list;watch,namespace=placeholder

type Reconciler struct {
	client                client.Client
	cancel                context.CancelFunc
	loadedResourceVersion string
	shutdownOnce          sync.Once
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var cfg operatorv1.OperatorConfig
	if err := r.client.Get(ctx, req.NamespacedName, &cfg); err != nil {
		if apierrors.IsNotFound(err) && r.loadedResourceVersion != "" {
			zap.S().Info("OperatorConfig deleted — initiating graceful shutdown to revert to defaults")
			r.shutdownOnce.Do(r.cancel)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if cfg.ResourceVersion == r.loadedResourceVersion {
		return reconcile.Result{}, nil
	}
	zap.S().Info("OperatorConfig changed — initiating graceful shutdown for config reload")
	r.shutdownOnce.Do(r.cancel)
	return reconcile.Result{}, nil
}

func AddOperatorConfigController(mgr manager.Manager, cancel context.CancelFunc, loadedResourceVersion string) error {
	r := &Reconciler{
		client:                mgr.GetClient(),
		cancel:                cancel,
		loadedResourceVersion: loadedResourceVersion,
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("operatorconfig").
		For(&operatorv1.OperatorConfig{}).
		Complete(r)
}
