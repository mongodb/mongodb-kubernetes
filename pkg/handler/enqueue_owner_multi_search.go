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

// Cross-cluster enqueue labels for search-owned member-cluster resources.
// Owner references do not cross cluster boundaries; both the search controller
// and the Envoy controller stamp these labels on every member-cluster write so
// mappers / predicates can enqueue the central MongoDBSearch request.
const (
	MongoDBSearchOwnerNameLabel      = "mongodb.com/search-name"
	MongoDBSearchOwnerNamespaceLabel = "mongodb.com/search-namespace"
	// MongoDBSearchClusterNameLabel records the owning member cluster on
	// per-cluster member resources (Envoy Deployment + ConfigMap).
	MongoDBSearchClusterNameLabel = "mongodb.com/cluster-name"
)

var _ handler.EventHandler = &EnqueueRequestForSearchOwnerMultiCluster{}

// EnqueueRequestForSearchOwnerMultiCluster enqueues reconcile requests for the
// MongoDBSearch CR identified by the search-owner labels on a watched
// member-cluster resource.
type EnqueueRequestForSearchOwnerMultiCluster struct{}

func (e *EnqueueRequestForSearchOwnerMultiCluster) Create(_ context.Context, evt event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if req := MapMemberClusterObjectToSearch(evt.Object); req != (reconcile.Request{}) {
		q.Add(req)
	}
}

func (e *EnqueueRequestForSearchOwnerMultiCluster) Update(_ context.Context, evt event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	for _, req := range []reconcile.Request{
		MapMemberClusterObjectToSearch(evt.ObjectOld),
		MapMemberClusterObjectToSearch(evt.ObjectNew),
	} {
		if req != (reconcile.Request{}) {
			q.Add(req)
		}
	}
}

func (e *EnqueueRequestForSearchOwnerMultiCluster) Delete(_ context.Context, evt event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if req := MapMemberClusterObjectToSearch(evt.Object); req != (reconcile.Request{}) {
		q.Add(req)
	}
}

func (e *EnqueueRequestForSearchOwnerMultiCluster) Generic(context.Context, event.TypedGenericEvent[client.Object], workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

// MapMemberClusterObjectToSearch reads the search-owner labels off a watched
// member-cluster object and returns the reconcile request for the central
// MongoDBSearch CR. Returns the zero Request when either label is missing.
func MapMemberClusterObjectToSearch(obj client.Object) reconcile.Request {
	labels := obj.GetLabels()
	name := labels[MongoDBSearchOwnerNameLabel]
	ns := labels[MongoDBSearchOwnerNamespaceLabel]
	if name == "" || ns == "" {
		return reconcile.Request{}
	}
	return reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
}
