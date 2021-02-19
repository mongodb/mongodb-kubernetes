package operator

import (
	"sync"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

// MongoDBResourceEventHandler is a custom event handler that extends the
// handler.EnqueueRequestForObject event handler. It overrides the Delete
// method used to clean up the mongodb resource when a deletion event happens.
// This results in a single, synchronous attempt to clean up the resource
// rather than an asynchronous one.
type MongoDBResourceEventHandler struct {
	*handler.EnqueueRequestForObject
	reconciler interface {
		delete(obj interface{}, log *zap.SugaredLogger) error
		GetMutex(resourceName types.NamespacedName) *sync.Mutex
	}
}

func (eh *MongoDBResourceEventHandler) Delete(e event.DeleteEvent, _ workqueue.RateLimitingInterface) {
	objectKey := kube.ObjectKey(e.Object.GetNamespace(), e.Object.GetName())
	logger := zap.S().With("resource", objectKey)

	// Reusing the lock used during update reconciliations
	mutex := eh.reconciler.GetMutex(objectKey)
	mutex.Lock()
	defer mutex.Unlock()

	zap.S().Infow("Cleaning up MongoDB resource", "resource", e.Object)
	if err := eh.reconciler.delete(e.Object, logger); err != nil {
		logger.Errorf("MongoDB resource removed from Kubernetes, but failed to clean some state in Ops Manager: %s", err)
		return
	}
	logger.Info("Removed MongoDB resource from Kubernetes and Ops Manager")
}
