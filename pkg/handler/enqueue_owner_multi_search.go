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
//
// Owner references do not cross cluster boundaries, so member-cluster watches
// (StatefulSet / Service / Deployment / ConfigMap / Secret) cannot rely on
// ownerRef to map back to the central MongoDBSearch CR. Both the search
// controller and the Envoy controller stamp these two labels on every write
// they make into a member cluster; mappers / predicates use them to enqueue
// the right central request.
//
// Search resources never cross namespaces — the namespace label exists so
// the mapper does not silently route across namespaces if the same name
// happens to repeat.
//
// MongoDBSearchOwnerNameLabel mirrors the legacy `mongodb.com/search-name`
// constant the Envoy controller uses internally; that controller-private
// constant must remain string-equal to this one.
const (
	MongoDBSearchOwnerNameLabel      = "mongodb.com/search-name"
	MongoDBSearchOwnerNamespaceLabel = "mongodb.com/search-namespace"
)

var _ handler.EventHandler = &EnqueueRequestForSearchOwnerMultiCluster{}

// EnqueueRequestForSearchOwnerMultiCluster enqueues reconcile requests for the
// MongoDBSearch CR identified by the search-owner labels on the watched
// resource. Owner references do not cross clusters, so we use this label
// pattern (mirrors mapEnvoyObjectToSearch in the Envoy controller).
//
// Phase 2 NOTE: this handler is wired to the search controller's
// member-cluster watches in AddMongoDBSearchController, but ga-base does not
// yet write any per-cluster member resources. Until Phase 2 stamps these
// labels at the member-cluster write sites in
// controllers/searchcontroller/mongodbsearch_reconcile_helper.go, the
// watches see no traffic — the routing is correct but inert.
type EnqueueRequestForSearchOwnerMultiCluster struct{}

func (e *EnqueueRequestForSearchOwnerMultiCluster) Create(_ context.Context, evt event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if req := mapMemberClusterObjectToSearch(evt.Object); req != (reconcile.Request{}) {
		q.Add(req)
	}
}

func (e *EnqueueRequestForSearchOwnerMultiCluster) Update(_ context.Context, evt event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	for _, req := range []reconcile.Request{
		mapMemberClusterObjectToSearch(evt.ObjectOld),
		mapMemberClusterObjectToSearch(evt.ObjectNew),
	} {
		if req != (reconcile.Request{}) {
			q.Add(req)
		}
	}
}

func (e *EnqueueRequestForSearchOwnerMultiCluster) Delete(_ context.Context, evt event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	if req := mapMemberClusterObjectToSearch(evt.Object); req != (reconcile.Request{}) {
		q.Add(req)
	}
}

func (e *EnqueueRequestForSearchOwnerMultiCluster) Generic(context.Context, event.TypedGenericEvent[client.Object], workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

// mapMemberClusterObjectToSearch reads the search-owner labels off a watched
// member-cluster object and returns the reconcile request for the central
// MongoDBSearch CR. Returns the zero Request when either label is missing —
// the caller filters those out before enqueueing.
//
// Shared with the Envoy controller's mapEnvoyObjectToSearch. Same labels,
// same semantics.
func mapMemberClusterObjectToSearch(obj client.Object) reconcile.Request {
	labels := obj.GetLabels()
	name := labels[MongoDBSearchOwnerNameLabel]
	ns := labels[MongoDBSearchOwnerNamespaceLabel]
	if name == "" || ns == "" {
		return reconcile.Request{}
	}
	return reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
}
