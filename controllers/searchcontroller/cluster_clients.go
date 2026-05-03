package searchcontroller

import (
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
)

// SelectClusterClient returns the Kubernetes client to use for the given cluster name.
//
// Rules:
//  1. If members is empty (single-cluster install where the operator was started without
//     mongodbmulticluster as a watched CRD), return central — single-cluster fallback.
//  2. If clusterName is empty (single-cluster spec.clusters[0] with no clusterName set),
//     return central — degenerate single-cluster mode.
//  3. If clusterName is present in members, return that client.
//  4. If clusterName is set but missing from members, return (nil, false) — this is a
//     configuration error and the caller must surface it as a status warning.
func SelectClusterClient(
	clusterName string,
	central kubernetesClient.Client,
	members map[string]kubernetesClient.Client,
) (kubernetesClient.Client, bool) {
	if len(members) == 0 || clusterName == "" {
		return central, true
	}
	if c, ok := members[clusterName]; ok {
		return c, true
	}
	return nil, false
}
