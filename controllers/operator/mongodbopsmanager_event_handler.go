package operator

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/kube"
	"go.uber.org/zap"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

// MongoDBOpsManagerEventHandler extends handler.EnqueueRequestForObject (from controller-runtime)
// which enqueues a Request containing the Name and Namespace of the object that is the source of the Event.
// It is used by the OpsManagerReconciler to reconcile OpsManager resource.
type MongoDBOpsManagerEventHandler struct {
	*handler.EnqueueRequestForObject
	reconciler interface {
		OnDelete(obj interface{}, log *zap.SugaredLogger)
	}
}

// Delete implements EventHandler and it is called when the CR is removed
func (eh *MongoDBOpsManagerEventHandler) Delete(e event.DeleteEvent, _ workqueue.RateLimitingInterface) {
	objectKey := kube.ObjectKey(e.Object.GetNamespace(), e.Object.GetName())
	logger := zap.S().With("resource", objectKey)

	zap.S().Infow("Cleaning up OpsManager resource", "resource", e.Object)
	eh.reconciler.OnDelete(e.Object, logger)

	logger.Info("Removed Ops Manager resource")
}
