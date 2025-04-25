package commoncontroller

import (
	"context"
	"time"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	kubernetesClient "github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/client"
)

// GetResource populates the provided runtime.Object with some additional error handling
func GetResource(ctx context.Context, kubeClient kubernetesClient.Client, request reconcile.Request, resource v1.CustomResourceReadWriter, log *zap.SugaredLogger) (reconcile.Result, error) {
	err := kubeClient.Get(ctx, request.NamespacedName, resource)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			log.Debugf("Object %s doesn't exist, was it deleted after reconcile request?", request.NamespacedName)
			return reconcile.Result{}, err
		}
		// Error reading the object - requeue the request.
		log.Errorf("Failed to query object %s: %s", request.NamespacedName, err)
		return reconcile.Result{RequeueAfter: 10 * time.Second}, err
	}
	return reconcile.Result{}, nil
}
