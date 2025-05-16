package operator

import (
	"context"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
)

// Deleter cleans up any state required upon deletion of a resource.
type Deleter interface {
	OnDelete(ctx context.Context, obj runtime.Object, log *zap.SugaredLogger) error
}

// ResourceEventHandler is a custom event handler that extends the
// handler.EnqueueRequestForObject event handler. It overrides the Delete
// method used to clean up custom resources when a deletion event happens.
// This results in a single, synchronous attempt to clean up the resource
// rather than an asynchronous one.
type ResourceEventHandler struct {
	*handler.EnqueueRequestForObject
	deleter Deleter
}

func (h *ResourceEventHandler) Delete(ctx context.Context, e event.DeleteEvent, _ workqueue.RateLimitingInterface) {
	objectKey := kube.ObjectKey(e.Object.GetNamespace(), e.Object.GetName())
	logger := zap.S().With("resource", objectKey)

	zap.S().Infow("Cleaning up Resource", "resource", e.Object)
	if err := h.deleter.OnDelete(ctx, e.Object, logger); err != nil {
		logger.Errorf("Resource removed from Kubernetes, but failed to clean some state in Ops Manager: %s", err)
		return
	}
	logger.Info("Removed Resource from Kubernetes and Ops Manager")
}
