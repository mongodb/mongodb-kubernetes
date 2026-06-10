package search

import "maps"

// AssignClusterIndices merges pinned spec.clusters[].ClusterIndex over existing (which is
// not mutated); removed clusters keep their persisted indices, unpinned new entries get 0.
func AssignClusterIndices(existing map[string]int, current []ClusterSpec) map[string]int {
	result := make(map[string]int, len(existing)+len(current))
	maps.Copy(result, existing)
	for _, c := range current {
		if c.ClusterIndex != nil {
			result[c.ClusterName] = int(*c.ClusterIndex)
		} else if _, ok := result[c.ClusterName]; !ok {
			result[c.ClusterName] = 0
		}
	}
	return result
}
