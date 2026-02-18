package searchcontroller

import (
	"testing"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
)

func newEnterpriseSearchSource(version string, topology string, resourceType mdbv1.ResourceType, authModes []string, internalClusterAuth string) EnterpriseResourceSearchSource {
	authModesList := make([]mdbv1.AuthMode, len(authModes))
	for i, mode := range authModes {
		authModesList[i] = mdbv1.AuthMode(mode)
	}

	// Create security with authentication if needed
	var security *mdbv1.Security
	if len(authModes) > 0 || internalClusterAuth != "" {
		security = &mdbv1.Security{
			Authentication: &mdbv1.Authentication{
				Enabled:         len(authModes) > 0,
				Modes:           authModesList,
				InternalCluster: internalClusterAuth,
			},
		}
	}

	src := EnterpriseResourceSearchSource{
		MongoDB: &mdbv1.MongoDB{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-mongodb",
				Namespace: "test-namespace",
			},
			Spec: mdbv1.MongoDbSpec{
				DbCommonSpec: mdbv1.DbCommonSpec{
					Version:      version,
					ResourceType: resourceType,
					Security:     security,
				},
			},
		},
	}

	// Set topology directly since it's inlined from DbCommonSpec
	src.Spec.Topology = topology
	return src
}

func TestEnterpriseResourceSearchSource_Validate(t *testing.T) {
	cases := []struct {
		name                string
		version             string
		topology            string
		resourceType        mdbv1.ResourceType
		authModes           []string
		internalClusterAuth string
		expectError         bool
		expectedErrMsg      string
	}{
		// Version validation tests
		{
			name:           "Invalid version",
			version:        "invalid.version",
			topology:       mdbv1.ClusterTopologySingleCluster,
			resourceType:   mdbv1.ReplicaSet,
			authModes:      []string{},
			expectError:    true,
			expectedErrMsg: "error parsing MongoDB version",
		},
		{
			name:           "Version too old",
			version:        "7.0.0",
			topology:       mdbv1.ClusterTopologySingleCluster,
			resourceType:   mdbv1.ReplicaSet,
			authModes:      []string{},
			expectError:    true,
			expectedErrMsg: "MongoDB version must be 8.2.0 or higher",
		},
		{
			name:           "Version just below minimum",
			version:        "8.1.9",
			topology:       mdbv1.ClusterTopologySingleCluster,
			resourceType:   mdbv1.ReplicaSet,
			authModes:      []string{},
			expectError:    true,
			expectedErrMsg: "MongoDB version must be 8.2.0 or higher",
		},
		{
			name:         "Valid minimum version",
			version:      "8.2.0",
			topology:     mdbv1.ClusterTopologySingleCluster,
			resourceType: mdbv1.ReplicaSet,
			authModes:    []string{},
			expectError:  false,
		},
		{
			name:         "Version above minimum",
			version:      "8.3.0",
			topology:     mdbv1.ClusterTopologySingleCluster,
			resourceType: mdbv1.ReplicaSet,
			authModes:    []string{},
			expectError:  false,
		},
		// Topology validation tests
		{
			name:           "Invalid topology - MultiCluster",
			version:        "8.2.0",
			topology:       mdbv1.ClusterTopologyMultiCluster,
			resourceType:   mdbv1.ReplicaSet,
			authModes:      []string{},
			expectError:    true,
			expectedErrMsg: "MongoDBSearch is only supported for SingleCluster topology",
		},
		{
			name:         "Valid topology - SingleCluster",
			version:      "8.2.0",
			topology:     mdbv1.ClusterTopologySingleCluster,
			resourceType: mdbv1.ReplicaSet,
			authModes:    []string{},
			expectError:  false,
		},
		{
			name:         "Empty topology defaults to SingleCluster",
			version:      "8.2.0",
			topology:     "",
			resourceType: mdbv1.ReplicaSet,
			authModes:    []string{},
			expectError:  false,
		},
		// Resource type validation tests
		{
			name:           "Invalid resource type - Standalone",
			version:        "8.2.0",
			topology:       mdbv1.ClusterTopologySingleCluster,
			resourceType:   mdbv1.Standalone,
			authModes:      []string{},
			expectError:    true,
			expectedErrMsg: "MongoDBSearch is only supported for ReplicaSet resources",
		},
		{
			name:           "Invalid resource type - ShardedCluster",
			version:        "8.2.0",
			topology:       mdbv1.ClusterTopologySingleCluster,
			resourceType:   mdbv1.ShardedCluster,
			authModes:      []string{},
			expectError:    true,
			expectedErrMsg: "MongoDBSearch is only supported for ReplicaSet resources",
		},
		{
			name:         "Valid resource type - ReplicaSet",
			version:      "8.2.0",
			topology:     mdbv1.ClusterTopologySingleCluster,
			resourceType: mdbv1.ReplicaSet,
			authModes:    []string{},
			expectError:  false,
		},
		// Authentication mode tests
		{
			name:           "No SCRAM authentication",
			version:        "8.2.0",
			topology:       mdbv1.ClusterTopologySingleCluster,
			resourceType:   mdbv1.ReplicaSet,
			authModes:      []string{"X509"},
			expectError:    true,
			expectedErrMsg: "MongoDBSearch requires SCRAM authentication to be enabled",
		},
		{
			name:         "Empty authentication modes",
			version:      "8.2.0",
			topology:     mdbv1.ClusterTopologySingleCluster,
			resourceType: mdbv1.ReplicaSet,
			authModes:    []string{},
			expectError:  false,
		},
		{
			name:         "Nil authentication modes",
			version:      "8.2.0",
			topology:     mdbv1.ClusterTopologySingleCluster,
			resourceType: mdbv1.ReplicaSet,
			authModes:    nil,
			expectError:  false,
		},
		{
			name:         "Valid SCRAM authentication",
			version:      "8.2.0",
			topology:     mdbv1.ClusterTopologySingleCluster,
			resourceType: mdbv1.ReplicaSet,
			authModes:    []string{"SCRAM-SHA-256"},
			expectError:  false,
		},
		{
			name:         "Mixed auth modes with SCRAM",
			version:      "8.2.0",
			topology:     mdbv1.ClusterTopologySingleCluster,
			resourceType: mdbv1.ReplicaSet,
			authModes:    []string{"X509", "SCRAM-SHA-256"},
			expectError:  false,
		},
		{
			name:         "Case insensitive SCRAM",
			version:      "8.2.0",
			topology:     mdbv1.ClusterTopologySingleCluster,
			resourceType: mdbv1.ReplicaSet,
			authModes:    []string{"scram-sha-256"},
			expectError:  false,
		},
		{
			name:         "SCRAM variants",
			version:      "8.2.0",
			topology:     mdbv1.ClusterTopologySingleCluster,
			resourceType: mdbv1.ReplicaSet,
			authModes:    []string{"SCRAM", "SCRAM-SHA-1", "SCRAM-SHA-256"},
			expectError:  false,
		},
		// Internal cluster authentication tests
		{
			name:                "X509 internal cluster auth not supported",
			version:             "8.2.0",
			topology:            mdbv1.ClusterTopologySingleCluster,
			resourceType:        mdbv1.ReplicaSet,
			authModes:           []string{"SCRAM-SHA-256"},
			internalClusterAuth: "X509",
			expectError:         false,
		},
		{
			name:                "Valid internal cluster auth - empty",
			version:             "8.2.0",
			topology:            mdbv1.ClusterTopologySingleCluster,
			resourceType:        mdbv1.ReplicaSet,
			authModes:           []string{"SCRAM-SHA-256"},
			internalClusterAuth: "",
			expectError:         false,
		},
		{
			name:                "Valid internal cluster auth - SCRAM",
			version:             "8.2.0",
			topology:            mdbv1.ClusterTopologySingleCluster,
			resourceType:        mdbv1.ReplicaSet,
			authModes:           []string{"SCRAM-SHA-256"},
			internalClusterAuth: "SCRAM",
			expectError:         false,
		},
		// Combined validation tests
		{
			name:           "Multiple validation failures - version takes precedence",
			version:        "7.0.0",
			topology:       mdbv1.ClusterTopologyMultiCluster,
			resourceType:   mdbv1.Standalone,
			authModes:      []string{"X509"},
			expectError:    true,
			expectedErrMsg: "MongoDB version must be 8.2.0 or higher",
		},
		{
			name:           "Valid version, invalid topology",
			version:        "8.2.0",
			topology:       mdbv1.ClusterTopologyMultiCluster,
			resourceType:   mdbv1.ReplicaSet,
			authModes:      []string{},
			expectError:    true,
			expectedErrMsg: "MongoDBSearch is only supported for SingleCluster topology",
		},
		{
			name:           "Valid version and topology, invalid resource type",
			version:        "8.2.0",
			topology:       mdbv1.ClusterTopologySingleCluster,
			resourceType:   mdbv1.Standalone,
			authModes:      []string{},
			expectError:    true,
			expectedErrMsg: "MongoDBSearch is only supported for ReplicaSet resources",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := newEnterpriseSearchSource(c.version, c.topology, c.resourceType, c.authModes, c.internalClusterAuth)
			err := src.Validate()

			if c.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), c.expectedErrMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func newShardedClusterMongoDB(name, namespace string, shardCount int, version string) *mdbv1.MongoDB {
	return &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mdbv1.MongoDbSpec{
			DbCommonSpec: mdbv1.DbCommonSpec{
				Version:      version,
				ResourceType: mdbv1.ShardedCluster,
			},
			MongodbShardedClusterSizeConfig: status.MongodbShardedClusterSizeConfig{
				ShardCount: shardCount,
			},
		},
	}
}

func newShardedExternalLBSearch(name, namespace, mdbName string, endpoints []searchv1.ShardEndpoint) *searchv1.MongoDBSearch {
	return &searchv1.MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.MongoDBSource{
				MongoDBResourceRef: &userv1.MongoDBResourceRef{
					Name: mdbName,
				},
				Replicas: 1,
			},
			LoadBalancer: &searchv1.LoadBalancerConfig{
				Mode: searchv1.LBModeUnmanaged,
				External: &searchv1.ExternalLBConfig{
					Sharded: &searchv1.ShardedExternalLBConfig{
						Endpoints: endpoints,
					},
				},
			},
		},
	}
}

func TestShardedEnterpriseSearchSource_GetShardNames(t *testing.T) {
	tests := []struct {
		name           string
		shardCount     int
		mdbName        string
		expectedShards []string
	}{
		{
			name:           "Single shard",
			shardCount:     1,
			mdbName:        "my-cluster",
			expectedShards: []string{"my-cluster-0"},
		},
		{
			name:           "Three shards",
			shardCount:     3,
			mdbName:        "my-cluster",
			expectedShards: []string{"my-cluster-0", "my-cluster-1", "my-cluster-2"},
		},
		{
			name:           "Five shards",
			shardCount:     5,
			mdbName:        "test-sharded",
			expectedShards: []string{"test-sharded-0", "test-sharded-1", "test-sharded-2", "test-sharded-3", "test-sharded-4"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mdb := newShardedClusterMongoDB(tc.mdbName, "test-ns", tc.shardCount, "8.2.0")
			search := newShardedExternalLBSearch("test-search", "test-ns", tc.mdbName, nil)
			src := NewShardedEnterpriseSearchSource(mdb, search)

			shardNames := src.GetShardNames()
			assert.Equal(t, tc.expectedShards, shardNames)
		})
	}
}

func TestShardedEnterpriseSearchSource_GetExternalLBEndpointForShard(t *testing.T) {
	endpoints := []searchv1.ShardEndpoint{
		{ShardName: "my-cluster-0", Endpoint: "lb0.example.com:27028"},
		{ShardName: "my-cluster-1", Endpoint: "lb1.example.com:27028"},
		{ShardName: "my-cluster-2", Endpoint: "lb2.example.com:27028"},
	}

	mdb := newShardedClusterMongoDB("my-cluster", "test-ns", 3, "8.2.0")
	search := newShardedExternalLBSearch("test-search", "test-ns", "my-cluster", endpoints)
	src := NewShardedEnterpriseSearchSource(mdb, search)

	tests := []struct {
		shardName        string
		expectedEndpoint string
	}{
		{"my-cluster-0", "lb0.example.com:27028"},
		{"my-cluster-1", "lb1.example.com:27028"},
		{"my-cluster-2", "lb2.example.com:27028"},
		{"my-cluster-3", ""}, // Not in endpoint map
		{"unknown-shard", ""},
	}

	for _, tc := range tests {
		t.Run(tc.shardName, func(t *testing.T) {
			endpoint := src.GetExternalLBEndpointForShard(tc.shardName)
			assert.Equal(t, tc.expectedEndpoint, endpoint)
		})
	}
}

func TestShardedEnterpriseSearchSource_Validate(t *testing.T) {
	tests := []struct {
		name           string
		shardCount     int
		endpoints      []searchv1.ShardEndpoint
		expectError    bool
		expectedErrMsg string
	}{
		{
			name:       "Valid - all shards have endpoints",
			shardCount: 2,
			endpoints: []searchv1.ShardEndpoint{
				{ShardName: "my-cluster-0", Endpoint: "lb0.example.com:27028"},
				{ShardName: "my-cluster-1", Endpoint: "lb1.example.com:27028"},
			},
			expectError: false,
		},
		{
			name:       "Invalid - missing endpoint for shard",
			shardCount: 3,
			endpoints: []searchv1.ShardEndpoint{
				{ShardName: "my-cluster-0", Endpoint: "lb0.example.com:27028"},
				{ShardName: "my-cluster-1", Endpoint: "lb1.example.com:27028"},
				// Missing my-cluster-2
			},
			expectError:    true,
			expectedErrMsg: "missing LB endpoints for shards",
		},
		{
			name:       "Empty endpoints - treated as non-sharded LB (no validation error)",
			shardCount: 2,
			endpoints:  []searchv1.ShardEndpoint{},
			// When endpoints is empty, IsShardedExternalLB() returns false,
			// so the shard endpoint validation is skipped
			expectError: false,
		},
		{
			name:       "Valid - extra endpoints are ignored",
			shardCount: 2,
			endpoints: []searchv1.ShardEndpoint{
				{ShardName: "my-cluster-0", Endpoint: "lb0.example.com:27028"},
				{ShardName: "my-cluster-1", Endpoint: "lb1.example.com:27028"},
				{ShardName: "my-cluster-2", Endpoint: "lb2.example.com:27028"}, // Extra
			},
			expectError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mdb := newShardedClusterMongoDB("my-cluster", "test-ns", tc.shardCount, "8.2.0")
			search := newShardedExternalLBSearch("test-search", "test-ns", "my-cluster", tc.endpoints)
			src := NewShardedEnterpriseSearchSource(mdb, search)

			err := src.Validate()

			if tc.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedErrMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
