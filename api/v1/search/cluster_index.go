package search

import (
	"encoding/json"
)

// AssignClusterIndices returns a new clusterName -> clusterIndex mapping that:
//   - preserves every entry in existing (never deletes a name, even if it is
//     not present in currentClusterNames — that's how indices are not reused
//     on remove/re-add);
//   - appends every name in currentClusterNames that is not already in the
//     mapping, assigning indices monotonically starting at max(existing)+1 (or
//     0 when existing is empty).
//
// existing is not mutated; callers may safely pass in the result of unmarshalling
// the persisted annotation. The returned map is always non-nil.
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

// ClusterIndex returns the persisted clusterIndex for the given clusterName by
// consulting the LastClusterNumMapping annotation on the MongoDBSearch CR. The
// second return value is false when the annotation is missing, empty, malformed,
// or does not contain the name.
//
// It deliberately reads only the persisted mapping — not spec.clusters — so a
// name that has been removed from the spec but is still recorded in the mapping
// continues to resolve.
func ClusterIndex(search *MongoDBSearch, clusterName string) (int, bool) {
	if search == nil {
		return 0, false
	}
	mapping := parseClusterMapping(search.Annotations[LastClusterNumMapping])
	idx, ok := mapping[clusterName]
	return idx, ok
}

// parseClusterMapping unmarshals the LastClusterNumMapping annotation value into
// a clusterName -> clusterIndex map. Returns nil when the value is empty or
// malformed (callers should treat both as "no mapping yet").
func parseClusterMapping(value string) map[string]int {
	if value == "" {
		return nil
	}
	m := make(map[string]int)
	if err := json.Unmarshal([]byte(value), &m); err != nil {
		return nil
	}
	return m
}
