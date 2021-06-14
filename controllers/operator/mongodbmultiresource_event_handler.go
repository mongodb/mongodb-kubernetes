package operator

import (
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

// MongoDBMultiResourceEventHandler is a custom event handler that extends the
// handler.EnqueueRequestForObject event handler.
// Note: currently we override the delete method for single cluster MongoDB replicaset
// we might need to override
type MongoDBMultiResourceEventHandler struct {
	*handler.EnqueueRequestForObject
	// reconciler interface {
	// 	delete(obj interface{}, log *zap.SugaredLogger) error
	// 	GetMutex(resourceName types.NamespacedName) *sync.Mutex
	// }
}
