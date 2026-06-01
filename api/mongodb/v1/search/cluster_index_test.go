package search

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAssignClusterIndices(t *testing.T) {
	pin := func(v int32) *int32 { return &v }
	names := func(ns ...string) []ClusterSpec {
		out := make([]ClusterSpec, 0, len(ns))
		for _, n := range ns {
			out = append(out, ClusterSpec{ClusterName: n})
		}
		return out
	}
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
			name:     "first assignment starts at 0",
			existing: map[string]int{},
			current:  names("us-east", "us-west"),
			want:     map[string]int{"us-east": 0, "us-west": 1},
		},
		{
			name:     "preserve existing on no-op",
			existing: map[string]int{"us-east": 0, "us-west": 1},
			current:  names("us-east", "us-west"),
			want:     map[string]int{"us-east": 0, "us-west": 1},
		},
		{
			name:     "append new monotonically from max+1",
			existing: map[string]int{"us-east": 0, "us-west": 1},
			current:  names("us-east", "us-west", "eu-central"),
			want:     map[string]int{"us-east": 0, "us-west": 1, "eu-central": 2},
		},
		{
			name:     "non-contiguous existing — next index is max+1, not gap",
			existing: map[string]int{"us-east": 0, "us-west": 5},
			current:  names("us-east", "us-west", "eu-central"),
			want:     map[string]int{"us-east": 0, "us-west": 5, "eu-central": 6},
		},
		{
			name:     "removed cluster stays in map (no reuse)",
			existing: map[string]int{"us-east": 0, "us-west": 1},
			current:  names("us-east"),
			want:     map[string]int{"us-east": 0, "us-west": 1},
		},
		{
			name:     "remove and re-add reuses the original index",
			existing: map[string]int{"us-east": 0, "us-west": 1},
			current:  names("us-east", "us-west"),
			want:     map[string]int{"us-east": 0, "us-west": 1},
		},
		{
			name:     "remove then add a different name uses next-after-max",
			existing: map[string]int{"us-east": 0, "us-west": 1},
			current:  names("us-east", "eu-central"),
			want:     map[string]int{"us-east": 0, "us-west": 1, "eu-central": 2},
		},
		{
			name:     "pinned ClusterIndex honored on first assignment",
			existing: map[string]int{},
			current:  []ClusterSpec{{ClusterName: "us-east", ClusterIndex: pin(7)}, {ClusterName: "us-west", ClusterIndex: pin(3)}},
			want:     map[string]int{"us-east": 7, "us-west": 3},
		},
		{
			name:     "pinned ClusterIndex overrides existing",
			existing: map[string]int{"us-east": 0, "us-west": 1},
			current:  []ClusterSpec{{ClusterName: "us-east", ClusterIndex: pin(9)}, {ClusterName: "us-west"}},
			want:     map[string]int{"us-east": 9, "us-west": 1},
		},
		{
			name:     "mixed pinned + unpinned: pin honored, rest monotonic from max",
			existing: map[string]int{},
			current:  []ClusterSpec{{ClusterName: "us-east", ClusterIndex: pin(5)}, {ClusterName: "us-west"}},
			want:     map[string]int{"us-east": 5, "us-west": 6},
		},
		{
			// Two distinct names pinning the same index: AssignClusterIndices keeps
			// both at that index (it keys on name, not index). Uniqueness across
			// clusterIndex values is a CRD CEL rule enforced at admission, not here.
			name:     "duplicate pinned index across distinct names: both kept (CEL enforces uniqueness at admission)",
			existing: map[string]int{},
			current:  []ClusterSpec{{ClusterName: "us-east", ClusterIndex: pin(5)}, {ClusterName: "us-west", ClusterIndex: pin(5)}},
			want:     map[string]int{"us-east": 5, "us-west": 5},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AssignClusterIndices(tt.existing, tt.current)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAssignClusterIndicesDoesNotMutateExisting(t *testing.T) {
	existing := map[string]int{"a": 0, "b": 1}
	AssignClusterIndices(existing, []ClusterSpec{{ClusterName: "a"}, {ClusterName: "b"}, {ClusterName: "c"}})
	assert.Equal(t, map[string]int{"a": 0, "b": 1}, existing, "existing must not be mutated")
}
