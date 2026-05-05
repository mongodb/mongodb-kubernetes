package search

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAssignClusterIndices(t *testing.T) {
	tests := []struct {
		name     string
		existing map[string]int
		current  []string
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
			current:  []string{"us-east", "us-west"},
			want:     map[string]int{"us-east": 0, "us-west": 1},
		},
		{
			name:     "preserve existing on no-op",
			existing: map[string]int{"us-east": 0, "us-west": 1},
			current:  []string{"us-east", "us-west"},
			want:     map[string]int{"us-east": 0, "us-west": 1},
		},
		{
			name:     "append new monotonically from max+1",
			existing: map[string]int{"us-east": 0, "us-west": 1},
			current:  []string{"us-east", "us-west", "eu-central"},
			want:     map[string]int{"us-east": 0, "us-west": 1, "eu-central": 2},
		},
		{
			name:     "non-contiguous existing — next index is max+1, not gap",
			existing: map[string]int{"us-east": 0, "us-west": 5},
			current:  []string{"us-east", "us-west", "eu-central"},
			want:     map[string]int{"us-east": 0, "us-west": 5, "eu-central": 6},
		},
		{
			name:     "removed cluster stays in map (no reuse)",
			existing: map[string]int{"us-east": 0, "us-west": 1},
			current:  []string{"us-east"},
			want:     map[string]int{"us-east": 0, "us-west": 1},
		},
		{
			name:     "remove and re-add reuses the original index",
			existing: map[string]int{"us-east": 0, "us-west": 1},
			current:  []string{"us-east", "us-west"},
			want:     map[string]int{"us-east": 0, "us-west": 1},
		},
		{
			name:     "remove then add a different name uses next-after-max",
			existing: map[string]int{"us-east": 0, "us-west": 1},
			current:  []string{"us-east", "eu-central"},
			want:     map[string]int{"us-east": 0, "us-west": 1, "eu-central": 2},
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
	AssignClusterIndices(existing, []string{"a", "b", "c"})
	assert.Equal(t, map[string]int{"a": 0, "b": 1}, existing, "existing must not be mutated")
}

