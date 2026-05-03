package handler

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// MongoDBSearchResourceAnnotation is set on resources the MongoDBSearch
// controller writes into a member cluster. It carries the name of the owning
// MongoDBSearch CR in the central cluster. The owner namespace equals the
// annotated object's own namespace — search resources never cross namespaces.
//
// This is a separate annotation from MongoDBMultiResourceAnnotation:
// MongoDBMultiCluster and MongoDBSearch may coexist in the same namespace,
// and event routing must not collide.
const MongoDBSearchResourceAnnotation = "mongodb.com/v1.MongoDBSearchResource"

var _ handler.EventHandler = &EnqueueRequestForSearchOwnerMultiCluster{}

// EnqueueRequestForSearchOwnerMultiCluster enqueues reconcile requests for the
// MongoDBSearch CR identified by MongoDBSearchResourceAnnotation on the watched
// resource. Owner references do not cross clusters, so we use this annotation
// pattern (mirrors EnqueueRequestForOwnerMultiCluster).
type EnqueueRequestForSearchOwnerMultiCluster struct{}

func (e *EnqueueRequestForSearchOwnerMultiCluster) Create(_ context.Context, evt event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if req := getOwnerSearchCRD(evt.Object.GetAnnotations(), evt.Object.GetNamespace()); req != (reconcile.Request{}) {
		q.Add(req)
	}
}

func (e *EnqueueRequestForSearchOwnerMultiCluster) Update(_ context.Context, evt event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	for _, req := range []reconcile.Request{
		getOwnerSearchCRD(evt.ObjectOld.GetAnnotations(), evt.ObjectOld.GetNamespace()),
		getOwnerSearchCRD(evt.ObjectNew.GetAnnotations(), evt.ObjectNew.GetNamespace()),
	} {
		if req != (reconcile.Request{}) {
			q.Add(req)
		}
	}
}

func (e *EnqueueRequestForSearchOwnerMultiCluster) Delete(_ context.Context, evt event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if req := getOwnerSearchCRD(evt.Object.GetAnnotations(), evt.Object.GetNamespace()); req != (reconcile.Request{}) {
		q.Add(req)
	}
}

func (e *EnqueueRequestForSearchOwnerMultiCluster) Generic(context.Context, event.TypedGenericEvent[client.Object], workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func getOwnerSearchCRD(annotations map[string]string, namespace string) reconcile.Request {
	val, ok := annotations[MongoDBSearchResourceAnnotation]
	if !ok {
		return reconcile.Request{}
	}
	return reconcile.Request{NamespacedName: types.NamespacedName{Name: val, Namespace: namespace}}
}
