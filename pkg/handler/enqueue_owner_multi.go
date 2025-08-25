package handler

import (
	"context"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const MongoDBMultiResourceAnnotation = "MongoDBMultiResource"

var _ handler.EventHandler = &EnqueueRequestForOwnerMultiCluster{}

// EnqueueRequestForOwnerMultiCluster implements the EventHandler interface for multi-cluster callbacks.
// We cannot reuse the "EnqueueRequestForOwner" because it uses OwnerReference which doesn't work across clusters
type EnqueueRequestForOwnerMultiCluster struct{}

func (e *EnqueueRequestForOwnerMultiCluster) Create(ctx context.Context, evt event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	req := getOwnerMDBCRD(evt.Object.GetAnnotations(), evt.Object.GetNamespace())
	if req != (reconcile.Request{}) {
		q.Add(req)
	}
}

func (e *EnqueueRequestForOwnerMultiCluster) Update(ctx context.Context, evt event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	reqs := []reconcile.Request{
		getOwnerMDBCRD(evt.ObjectOld.GetAnnotations(), evt.ObjectOld.GetNamespace()),
		getOwnerMDBCRD(evt.ObjectNew.GetAnnotations(), evt.ObjectNew.GetNamespace()),
	}

	for _, req := range reqs {
		if req != (reconcile.Request{}) {
			q.Add(req)
		}
	}
}

func (e *EnqueueRequestForOwnerMultiCluster) Delete(ctx context.Context, evt event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	req := getOwnerMDBCRD(evt.Object.GetAnnotations(), evt.Object.GetNamespace())
	q.Add(req)
}

func (e *EnqueueRequestForOwnerMultiCluster) Generic(context.Context, event.TypedGenericEvent[client.Object], workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func getOwnerMDBCRD(annotations map[string]string, namespace string) reconcile.Request {
	val, ok := annotations[MongoDBMultiResourceAnnotation]
	if !ok {
		return reconcile.Request{}
	}
	return reconcile.Request{NamespacedName: types.NamespacedName{Name: val, Namespace: namespace}}
}
