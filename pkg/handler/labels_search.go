package handler

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// EnqueueMemberClusterObjectToSearch is the handler.MapFunc wrapper around
// MapMemberClusterObjectToSearch, shared by both search controllers to enqueue
// the central MongoDBSearch from member-cluster resource events.
func EnqueueMemberClusterObjectToSearch(_ context.Context, obj client.Object) []reconcile.Request {
	req := MapMemberClusterObjectToSearch(obj)
	if req == (reconcile.Request{}) {
		return nil
	}
	return []reconcile.Request{req}
}
