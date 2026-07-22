package searchcontroller

import (
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

// SearchClusterRouter resolves which Kubernetes client serves each
// spec.clusters entry. It is the single source of truth for search cluster
// locality, shared by the search reconcilers and the reconcile helper.
type SearchClusterRouter struct {
	central kubernetesClient.Client
	members map[string]kubernetesClient.Client

	// NamedClustersAreLocal is true when named spec.clusters entries are served
	// by this operator's own cluster: per-cluster-operator mode and
	// single-cluster installs. Only hub-and-spoke installs (member clients
	// registered, no operator cluster name) route named clusters to members.
	NamedClustersAreLocal bool
}

func NewSearchClusterRouter(central kubernetesClient.Client, members map[string]kubernetesClient.Client, operatorClusterName string) SearchClusterRouter {
	return SearchClusterRouter{
		central:               central,
		members:               members,
		NamedClustersAreLocal: operatorClusterName != "" || len(members) == 0,
	}
}

// IsLocalCluster reports whether clusterName's resources live on this
// operator's own cluster. The empty cluster name (single-cluster installs) is
// always local.
func (r SearchClusterRouter) IsLocalCluster(clusterName string) bool {
	return clusterName == "" || r.NamedClustersAreLocal
}

// ClientForCluster resolves the client serving one cluster. ok is false only
// for a named cluster in hub-and-spoke mode with no registered member client.
func (r SearchClusterRouter) ClientForCluster(clusterName string) (kubernetesClient.Client, bool) {
	if r.IsLocalCluster(clusterName) {
		return r.central, true
	}
	c, ok := r.members[clusterName]
	return c, ok
}
