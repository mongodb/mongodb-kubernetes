package operator

import (
	"fmt"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
)

// projectionResult is the outcome of projectToLocalCluster.
type projectionResult int

const (
	projLegacy    projectionResult = iota // caller runs the existing reconcile path
	projNoMatch                           // operator's clusterName isn't in spec.clusters[] → silent no-op
	projProjected                         // search.Spec.Clusters mutated to a 1-element slice; idx is the matched clusterIndex
)

// projectToLocalCluster narrows search.Spec.Clusters to the entry whose
// clusterName matches operatorClusterName, mutating in place on projProjected.
// v0 constraints: replica-set source only; every spec.clusters[i].clusterIndex
// must be set. When operatorClusterName == "" or spec.clusters is empty,
// returns projLegacy without mutation.
func projectToLocalCluster(search *searchv1.MongoDBSearch, operatorClusterName string) (projectionResult, int, error) {
	if operatorClusterName == "" || search.Spec.Clusters == nil || len(*search.Spec.Clusters) == 0 {
		return projLegacy, 0, nil
	}
	if search.IsExternalSourceSharded() {
		return 0, 0, fmt.Errorf("simulated multi-cluster mode v0 supports replica-set source only; spec.source.external.shardedCluster is not supported")
	}
	clusters := *search.Spec.Clusters
	matchIdx := -1
	for i := range clusters {
		if clusters[i].ClusterIndex == nil {
			return 0, 0, fmt.Errorf("simulated multi-cluster mode requires spec.clusters[%d].clusterIndex to be set (cluster %q)", i, clusters[i].ClusterName)
		}
		if clusters[i].ClusterName == operatorClusterName {
			matchIdx = i
		}
	}
	if matchIdx == -1 {
		return projNoMatch, 0, nil
	}
	matched := clusters[matchIdx]
	only := []searchv1.ClusterSpec{matched}
	search.Spec.Clusters = &only
	return projProjected, int(*matched.ClusterIndex), nil
}
