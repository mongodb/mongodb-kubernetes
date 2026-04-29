package searchcontroller

import (
	"testing"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/mongot"
)

func newSearchWithClusters(clusters []searchv1.ClusterSpec) *searchv1.MongoDBSearch {
	cs := clusters
	return &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Spec:       searchv1.MongoDBSearchSpec{Clusters: &cs},
	}
}

func TestReadPreferenceTagsMod(t *testing.T) {
	tests := []struct {
		name        string
		search      *searchv1.MongoDBSearch
		clusterName string
		expectTags  []map[string]string
	}{
		{
			name:        "empty clusterName is NOOP",
			search:      newSearchWithClusters([]searchv1.ClusterSpec{{ClusterName: "us-east-k8s", SyncSourceSelector: &searchv1.SyncSourceSelector{MatchTags: map[string]string{"region": "us-east"}}}}),
			clusterName: "",
			expectTags:  nil,
		},
		{
			name:        "nil Clusters is NOOP (single-cluster back-compat)",
			search:      &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"}},
			clusterName: "us-east-k8s",
			expectTags:  nil,
		},
		{
			name: "matchTags set on matching cluster emits readPreferenceTags",
			search: newSearchWithClusters([]searchv1.ClusterSpec{
				{ClusterName: "us-east-k8s", SyncSourceSelector: &searchv1.SyncSourceSelector{MatchTags: map[string]string{"region": "us-east"}}},
				{ClusterName: "eu-west-k8s", SyncSourceSelector: &searchv1.SyncSourceSelector{MatchTags: map[string]string{"region": "eu-west"}}},
			}),
			clusterName: "us-east-k8s",
			expectTags:  []map[string]string{{"region": "us-east"}},
		},
		{
			name: "no syncSourceSelector on the matching cluster is NOOP",
			search: newSearchWithClusters([]searchv1.ClusterSpec{
				{ClusterName: "us-east-k8s"},
			}),
			clusterName: "us-east-k8s",
			expectTags:  nil,
		},
		{
			name: "empty matchTags is NOOP",
			search: newSearchWithClusters([]searchv1.ClusterSpec{
				{ClusterName: "us-east-k8s", SyncSourceSelector: &searchv1.SyncSourceSelector{MatchTags: map[string]string{}}},
			}),
			clusterName: "us-east-k8s",
			expectTags:  nil,
		},
		{
			name: "clusterName not found in spec.clusters is NOOP",
			search: newSearchWithClusters([]searchv1.ClusterSpec{
				{ClusterName: "us-east-k8s", SyncSourceSelector: &searchv1.SyncSourceSelector{MatchTags: map[string]string{"region": "us-east"}}},
			}),
			clusterName: "ap-south-k8s",
			expectTags:  nil,
		},
		{
			name: "hosts (not matchTags) on matching cluster is NOOP for tags renderer",
			search: newSearchWithClusters([]searchv1.ClusterSpec{
				{ClusterName: "us-east-k8s", SyncSourceSelector: &searchv1.SyncSourceSelector{Hosts: []string{"mongo-1:27017"}}},
			}),
			clusterName: "us-east-k8s",
			expectTags:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := mongot.Config{}
			readPreferenceTagsMod(tt.search, tt.clusterName)(&cfg)
			assert.Equal(t, tt.expectTags, cfg.SyncSource.ReplicaSet.ReadPreferenceTags)
		})
	}
}

func TestSyncSourceHostsMod(t *testing.T) {
	tests := []struct {
		name             string
		search           *searchv1.MongoDBSearch
		clusterName      string
		initialReplicaHP []string
		initialRouter    *mongot.ConfigRouter
		expectReplicaHP  []string
		expectRouterHP   []string
	}{
		{
			name:             "empty clusterName is NOOP",
			search:           newSearchWithClusters([]searchv1.ClusterSpec{{ClusterName: "us-east-k8s", SyncSourceSelector: &searchv1.SyncSourceSelector{Hosts: []string{"mongo-east-1:27017"}}}}),
			clusterName:      "",
			initialReplicaHP: []string{"orig:27017"},
			expectReplicaHP:  []string{"orig:27017"},
		},
		{
			name:             "nil Clusters is NOOP",
			search:           &searchv1.MongoDBSearch{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"}},
			clusterName:      "us-east-k8s",
			initialReplicaHP: []string{"orig:27017"},
			expectReplicaHP:  []string{"orig:27017"},
		},
		{
			name: "hosts replaces ReplicaSet.HostAndPort",
			search: newSearchWithClusters([]searchv1.ClusterSpec{
				{ClusterName: "us-east-k8s", SyncSourceSelector: &searchv1.SyncSourceSelector{Hosts: []string{"mongo-east-1:27017", "mongo-east-2:27017"}}},
			}),
			clusterName:      "us-east-k8s",
			initialReplicaHP: []string{"orig:27017"},
			expectReplicaHP:  []string{"mongo-east-1:27017", "mongo-east-2:27017"},
		},
		{
			name: "hosts replaces Router.HostAndPort when router is present (sharded path)",
			search: newSearchWithClusters([]searchv1.ClusterSpec{
				{ClusterName: "us-east-k8s", SyncSourceSelector: &searchv1.SyncSourceSelector{Hosts: []string{"mongo-east-1:27017"}}},
			}),
			clusterName:      "us-east-k8s",
			initialReplicaHP: []string{"orig:27017"},
			initialRouter:    &mongot.ConfigRouter{HostAndPort: []string{"mongos-orig:27017"}},
			expectReplicaHP:  []string{"mongo-east-1:27017"},
			expectRouterHP:   []string{"mongo-east-1:27017"},
		},
		{
			name: "matchTags only (no hosts) is NOOP",
			search: newSearchWithClusters([]searchv1.ClusterSpec{
				{ClusterName: "us-east-k8s", SyncSourceSelector: &searchv1.SyncSourceSelector{MatchTags: map[string]string{"region": "us-east"}}},
			}),
			clusterName:      "us-east-k8s",
			initialReplicaHP: []string{"orig:27017"},
			expectReplicaHP:  []string{"orig:27017"},
		},
		{
			name: "clusterName not found is NOOP",
			search: newSearchWithClusters([]searchv1.ClusterSpec{
				{ClusterName: "us-east-k8s", SyncSourceSelector: &searchv1.SyncSourceSelector{Hosts: []string{"mongo-east-1:27017"}}},
			}),
			clusterName:      "ap-south-k8s",
			initialReplicaHP: []string{"orig:27017"},
			expectReplicaHP:  []string{"orig:27017"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := mongot.Config{}
			cfg.SyncSource.ReplicaSet.HostAndPort = tt.initialReplicaHP
			cfg.SyncSource.Router = tt.initialRouter
			syncSourceHostsMod(tt.search, tt.clusterName)(&cfg)
			assert.Equal(t, tt.expectReplicaHP, cfg.SyncSource.ReplicaSet.HostAndPort)
			if tt.expectRouterHP != nil {
				if assert.NotNil(t, cfg.SyncSource.Router) {
					assert.Equal(t, tt.expectRouterHP, cfg.SyncSource.Router.HostAndPort)
				}
			}
		})
	}
}
