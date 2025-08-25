package watch

import (
	"context"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/contains"
)

// ResourceWatcher implements handler.EventHandler and is used to trigger reconciliation when
// a watched object changes. It's designed to only be used for a single type of object.
// If multiple types should be watched, one ResourceWatcher for each type should be used.
type ResourceWatcher struct {
	watched map[types.NamespacedName][]types.NamespacedName
}

var _ handler.EventHandler = &ResourceWatcher{}

// New will create a new ResourceWatcher with no watched objects.
func New() ResourceWatcher {
	return ResourceWatcher{
		watched: make(map[types.NamespacedName][]types.NamespacedName),
	}
}

// Watch will add a new object to watch.
func (w ResourceWatcher) Watch(ctx context.Context, watchedName, dependentName types.NamespacedName) {
	existing, hasExisting := w.watched[watchedName]
	if !hasExisting {
		existing = []types.NamespacedName{}
	}

	// Check if resource is already being watched.
	if contains.NamespacedName(existing, dependentName) {
		return
	}

	w.watched[watchedName] = append(existing, dependentName)
}

func (w ResourceWatcher) Create(ctx context.Context, event event.TypedCreateEvent[client.Object], queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	w.handleEvent(event.Object, queue)
}

func (w ResourceWatcher) Update(ctx context.Context, event event.TypedUpdateEvent[client.Object], queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	w.handleEvent(event.ObjectOld, queue)
}

func (w ResourceWatcher) Delete(ctx context.Context, event event.TypedDeleteEvent[client.Object], queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	w.handleEvent(event.Object, queue)
}

func (w ResourceWatcher) Generic(ctx context.Context, event event.TypedGenericEvent[client.Object], queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	w.handleEvent(event.Object, queue)
}

// handleEvent is called when an event is received for an object.
// It will check if the object is being watched and trigger a reconciliation for
// the dependent object.
func (w ResourceWatcher) handleEvent(meta metav1.Object, queue workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	changedObjectName := types.NamespacedName{
		Name:      meta.GetName(),
		Namespace: meta.GetNamespace(),
	}

	// Enqueue reconciliation for each dependent object.
	for _, reconciledObjectName := range w.watched[changedObjectName] {
		queue.Add(reconcile.Request{
			NamespacedName: reconciledObjectName,
		})
	}
}
