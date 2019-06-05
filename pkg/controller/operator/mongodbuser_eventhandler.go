package operator

import (
	"go.uber.org/zap"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

type MongoDBUserEventHandler struct {
	*handler.EnqueueRequestForObject
	reconciler interface {
		delete(obj interface{}, log *zap.SugaredLogger) error
	}
}

func (eh *MongoDBUserEventHandler) Delete(e event.DeleteEvent, _ workqueue.RateLimitingInterface) {
	zap.S().Infow("Cleaning up MongoDBUser resource", "resource", e.Object)
	logger := zap.S().With("resource", objectKey(e.Meta.GetNamespace(), e.Meta.GetName()))
	if err := eh.reconciler.delete(e.Object, logger); err != nil {
		logger.Errorf("MongoDBUser resource removed from Kubernetes, but failed to clean some state in Ops Manager: %s", err)
		return
	}
	logger.Info("Removed MongoDBUser resource from Kubernetes and Ops Manager")
}
