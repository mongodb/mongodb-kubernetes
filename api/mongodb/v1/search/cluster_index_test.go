package search

import (
	"testing"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestClusterIndex(t *testing.T) {
	withAnnotation := func(value string) *MongoDBSearch {
		return &MongoDBSearch{
			ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns", Annotations: map[string]string{LastClusterNumMapping: value}},
		}
	}
	tests := []struct {
		name        string
		search      *MongoDBSearch
		clusterName string
		wantIdx     int
		wantOK      bool
	}{
		{name: "nil search", search: nil, clusterName: "us-east", wantOK: false},
		{name: "no annotations", search: &MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "s"}}, clusterName: "us-east", wantOK: false},
		{name: "annotation missing", search: &MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "s", Annotations: map[string]string{}}}, clusterName: "us-east", wantOK: false},
		{name: "annotation empty string", search: withAnnotation(""), clusterName: "us-east", wantOK: false},
		{name: "annotation malformed JSON", search: withAnnotation("{not-json"), clusterName: "us-east", wantOK: false},
		{name: "name present", search: withAnnotation(`{"us-east":0,"us-west":1}`), clusterName: "us-east", wantIdx: 0, wantOK: true},
		{name: "name missing in valid mapping", search: withAnnotation(`{"us-east":0,"us-west":1}`), clusterName: "eu-central", wantOK: false},
		{name: "removed-but-still-mapped name returns persisted idx", search: withAnnotation(`{"us-east":0,"us-west":1}`), clusterName: "us-west", wantIdx: 1, wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIdx, gotOK := ClusterIndex(tt.search, tt.clusterName)
			assert.Equal(t, tt.wantOK, gotOK)
			if tt.wantOK {
				assert.Equal(t, tt.wantIdx, gotIdx)
			}
		})
	}
}
