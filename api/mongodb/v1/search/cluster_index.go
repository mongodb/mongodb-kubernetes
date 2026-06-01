package search

// AssignClusterIndices returns a new clusterName -> clusterIndex mapping that:
//   - preserves every entry in existing (never deletes a name, even if it is
//     not present in current — that's how indices are not reused on
//     remove/re-add);
//   - honors a user-pinned spec.clusters[].ClusterIndex: when set, that exact
//     index is used (this is what simulated multi-cluster relies on so per-cluster
//     resource names stay stable across the operator-per-cluster projection);
//   - appends every remaining (unpinned) name not already in the mapping,
//     assigning indices monotonically starting at max(existing/pinned)+1 (or 0
//     when nothing is set).
//
// existing is not mutated; callers may safely pass in a map from any persistence
// layer. The returned map is always non-nil.
func AssignClusterIndices(existing map[string]int, current []ClusterSpec) map[string]int {
	result := make(map[string]int, len(existing)+len(current))
	next := 0
	for k, v := range existing {
		result[k] = v
		if v >= next {
			next = v + 1
		}
	}
	for _, c := range current {
		if c.ClusterIndex == nil {
			continue
		}
		idx := int(*c.ClusterIndex)
		result[c.ClusterName] = idx
		if idx >= next {
			next = idx + 1
		}
	}
	for _, c := range current {
		if c.ClusterIndex != nil {
			continue
		}
		if _, ok := result[c.ClusterName]; ok {
			continue
		}
		result[c.ClusterName] = next
		next++
	}
	return result
}
