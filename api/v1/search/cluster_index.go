package search

// AssignClusterIndices returns a new clusterName -> clusterIndex mapping that:
//   - preserves every entry in existing (never deletes a name, even if it is
//     not present in currentClusterNames — that's how indices are not reused
//     on remove/re-add);
//   - appends every name in currentClusterNames that is not already in the
//     mapping, assigning indices monotonically starting at max(existing)+1 (or
//     0 when existing is empty).
//
// existing is not mutated; callers may safely pass in a map from any persistence
// layer. The returned map is always non-nil.
func AssignClusterIndices(existing map[string]int, currentClusterNames []string) map[string]int {
	result := make(map[string]int, len(existing)+len(currentClusterNames))
	next := 0
	for k, v := range existing {
		result[k] = v
		if v >= next {
			next = v + 1
		}
	}
	for _, name := range currentClusterNames {
		if _, ok := result[name]; ok {
			continue
		}
		result[name] = next
		next++
	}
	return result
}
