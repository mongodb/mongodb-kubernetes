package operator

import (
	"context"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"go.uber.org/zap"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
)

// MongoDBOpsManagerEventHandler extends handler.EnqueueRequestForObject (from controller-runtime)
// which enqueues a Request containing the Name and Namespace of the object that is the source of the Event.
// It is used by the OpsManagerReconciler to reconcile OpsManager resource.
type MongoDBOpsManagerEventHandler struct {
	*handler.EnqueueRequestForObject
	reconciler interface {
		OnDelete(ctx context.Context, obj interface{}, log *zap.SugaredLogger)
	}
}

// Delete implements EventHandler and it is called when the CR is removed
func (eh *MongoDBOpsManagerEventHandler) Delete(ctx context.Context, e event.TypedDeleteEvent[client.Object], _ workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	objectKey := kube.ObjectKey(e.Object.GetNamespace(), e.Object.GetName())
	logger := zap.S().With("resource", objectKey)

	zap.S().Infow("Cleaning up OpsManager resource", "resource", e.Object)
	eh.reconciler.OnDelete(ctx, e.Object, logger)

	logger.Info("Removed Ops Manager resource")
}
