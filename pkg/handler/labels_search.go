package handler

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Search resource identity labels used for routing and cleanup.
const (
	MongoDBSearchOwnerNameLabel      = "mongodb.com/search-name"
	MongoDBSearchOwnerNamespaceLabel = "mongodb.com/search-namespace"
	MongoDBSearchComponentLabel      = "component"
	// MongoDBSearchClusterNameLabel records the target member cluster.
	MongoDBSearchClusterNameLabel = "mongodb.com/cluster-name"
)

// SearchManagedLabels returns the managed identity labels shared by Search
// resource writers, cleanup selectors, and event routing. Writers merge user
// labels first and apply these labels last so user metadata overrides can
// never detach a resource from its owning MongoDBSearch.
func SearchManagedLabels(search metav1.Object, app, component, clusterName string) map[string]string {
	labels := map[string]string{
		MongoDBSearchOwnerNameLabel:      search.GetName(),
		MongoDBSearchOwnerNamespaceLabel: search.GetNamespace(),
	}
	if app != "" {
		labels["app"] = app
	}
	if component != "" {
		labels[MongoDBSearchComponentLabel] = component
	}
	if clusterName != "" {
		labels[MongoDBSearchClusterNameLabel] = clusterName
	}
	return labels
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
