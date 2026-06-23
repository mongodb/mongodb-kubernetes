package search

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"
)

func clusterSpecsNamed(ns ...string) []ClusterSpec {
	out := make([]ClusterSpec, 0, len(ns))
	for _, n := range ns {
		out = append(out, ClusterSpec{Name: n})
	}
	return out
}

func pinnedSpec(name string, idx int32) ClusterSpec {
	return ClusterSpec{Name: name, ClusterIndex: ptr.To(idx)}
}

func TestAssignClusterIndices(t *testing.T) {
	names := clusterSpecsNamed
	tests := []struct {
		name     string
		existing map[string]int
		current  []ClusterSpec
		want     map[string]int
	}{
		{
			name:     "empty existing, empty current",
			existing: map[string]int{},
			current:  nil,
			want:     map[string]int{},
		},
		{
			name:     "first assignment uses the pins",
			existing: map[string]int{},
			current:  []ClusterSpec{pinnedSpec("us-east", 0), pinnedSpec("us-west", 1)},
			want:     map[string]int{"us-east": 0, "us-west": 1},
		},
		{
			name:     "pinned no-op preserves existing",
			existing: map[string]int{"us-east": 0, "us-west": 1},
			current:  []ClusterSpec{pinnedSpec("us-east", 0), pinnedSpec("us-west", 1)},
			want:     map[string]int{"us-east": 0, "us-west": 1},
		},
		{
			name:     "append a new pinned entry",
			existing: map[string]int{"us-east": 0, "us-west": 1},
			current:  []ClusterSpec{pinnedSpec("us-east", 0), pinnedSpec("us-west", 1), pinnedSpec("eu-central", 2)},
			want:     map[string]int{"us-east": 0, "us-west": 1, "eu-central": 2},
		},
		{
			name:     "removed cluster stays in map (no reuse)",
			existing: map[string]int{"us-east": 0, "us-west": 1},
			current:  names("us-east"),
			want:     map[string]int{"us-east": 0, "us-west": 1},
		},
		{
			name:     "remove then add a different pinned name",
			existing: map[string]int{"us-east": 0, "us-west": 1},
			current:  []ClusterSpec{pinnedSpec("us-east", 0), pinnedSpec("eu-central", 2)},
			want:     map[string]int{"us-east": 0, "us-west": 1, "eu-central": 2},
		},
		{
			name:     "pinned ClusterIndex honored on first assignment",
			existing: map[string]int{},
			current:  []ClusterSpec{pinnedSpec("us-east", 7), pinnedSpec("us-west", 3)},
			want:     map[string]int{"us-east": 7, "us-west": 3},
		},
		{
			name:     "pinned ClusterIndex overrides existing",
			existing: map[string]int{"us-east": 0, "us-west": 1},
			current:  []ClusterSpec{pinnedSpec("us-east", 9), pinnedSpec("us-west", 1)},
			want:     map[string]int{"us-east": 9, "us-west": 1},
		},
		{
			name:     "single-entry unpinned keeps its existing index",
			existing: map[string]int{"us-east": 0, "us-west": 5},
			current:  names("us-west"),
			want:     map[string]int{"us-east": 0, "us-west": 5},
		},
		{
			name:     "single-entry unpinned with no mapping entry gets 0",
			existing: map[string]int{},
			current:  names("us-east"),
			want:     map[string]int{"us-east": 0},
		},
		{
			name:     "legacy single-cluster empty name gets 0",
			existing: map[string]int{},
			current:  []ClusterSpec{{}},
			want:     map[string]int{"": 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssignClusterIndices(tt.existing, tt.current)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Remove-then-re-add across successive reconciles: the retained mapping entry keeps the
// removed name's index claimed, and the re-added entry pins it back explicitly.
func TestAssignClusterIndicesRemoveAndReAddReusesIndex(t *testing.T) {
	assigned := AssignClusterIndices(map[string]int{}, []ClusterSpec{pinnedSpec("us-east", 0), pinnedSpec("us-west", 1)})
	assert.Equal(t, map[string]int{"us-east": 0, "us-west": 1}, assigned)

	afterRemove := AssignClusterIndices(assigned, clusterSpecsNamed("us-east"))
	assert.Equal(t, map[string]int{"us-east": 0, "us-west": 1}, afterRemove,
		"removed name must be retained so its index stays claimed")

	afterReAdd := AssignClusterIndices(afterRemove, []ClusterSpec{pinnedSpec("us-east", 0), pinnedSpec("us-west", 1)})
	assert.Equal(t, map[string]int{"us-east": 0, "us-west": 1}, afterReAdd,
		"re-added name must reuse its original index 1 via its pin")
}

func TestAssignClusterIndicesDoesNotMutateExisting(t *testing.T) {
	existing := map[string]int{"a": 0, "b": 1}
	AssignClusterIndices(existing, []ClusterSpec{pinnedSpec("a", 9)})
	assert.Equal(t, map[string]int{"a": 0, "b": 1}, existing, "existing must not be mutated")
}
