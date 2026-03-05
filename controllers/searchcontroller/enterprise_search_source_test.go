package searchcontroller

import (
	"testing"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

func newMongoDBResource(version string, topology string, resourceType mdbv1.ResourceType, authModes []string, internalClusterAuth string) *mdbv1.MongoDB {
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

	return &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mongodb",
			Namespace: "test-namespace",
		},
		Spec: mdbv1.MongoDbSpec{
			DbCommonSpec: mdbv1.DbCommonSpec{
				Version:      version,
				ResourceType: resourceType,
				Security:     security,
				Topology:     topology,
			},
		},
	}
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
			mdb := newMongoDBResource(c.version, c.topology, c.resourceType, c.authModes, c.internalClusterAuth)
			src := NewEnterpriseResourceSearchSource(mdb, nil)
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

func TestEnterpriseResourceSearchSource_HostSeeds(t *testing.T) {
	cases := []struct {
		name                 string
		mdbName              string
		mdbNamespace         string
		members              int
		externalMembers      []string
		clusterDomain        string
		processToHostnameMap map[string]om.HostnameAndPort
		expectedSeeds        []string
	}{
		{
			name:          "Single member, default cluster domain",
			mdbName:       "my-mdb",
			mdbNamespace:  "my-namespace",
			members:       1,
			expectedSeeds: []string{"my-mdb-0.my-mdb-svc.my-namespace.svc.cluster.local:27017"},
		},
		{
			name:         "Multiple members, default cluster domain",
			mdbName:      "my-mdb",
			mdbNamespace: "my-namespace",
			members:      3,
			expectedSeeds: []string{
				"my-mdb-0.my-mdb-svc.my-namespace.svc.cluster.local:27017",
				"my-mdb-1.my-mdb-svc.my-namespace.svc.cluster.local:27017",
				"my-mdb-2.my-mdb-svc.my-namespace.svc.cluster.local:27017",
			},
		},
		{
			name:          "Custom cluster domain",
			mdbName:       "my-mdb",
			mdbNamespace:  "my-namespace",
			members:       2,
			clusterDomain: "custom.domain",
			expectedSeeds: []string{
				"my-mdb-0.my-mdb-svc.my-namespace.svc.custom.domain:27017",
				"my-mdb-1.my-mdb-svc.my-namespace.svc.custom.domain:27017",
			},
		},
		{
			name:            "External members come before internal members",
			mdbName:         "my-mdb",
			mdbNamespace:    "my-namespace",
			members:         2,
			externalMembers: []string{"my-mdb-0", "my-mdb-1"},
			processToHostnameMap: map[string]om.HostnameAndPort{
				"my-mdb-0": {Hostname: "external-host-0.example.com", Port: 27017},
				"my-mdb-1": {Hostname: "external-host-1.example.com", Port: 27017},
			},
			expectedSeeds: []string{
				"external-host-0.example.com:27017",
				"external-host-1.example.com:27017",
				"my-mdb-0.my-mdb-svc.my-namespace.svc.cluster.local:27017",
				"my-mdb-1.my-mdb-svc.my-namespace.svc.cluster.local:27017",
			},
		},
		{
			name:          "No members returns empty slice",
			mdbName:       "my-mdb",
			mdbNamespace:  "my-namespace",
			members:       0,
			expectedSeeds: []string{},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mdb := &mdbv1.MongoDB{
				ObjectMeta: metav1.ObjectMeta{
					Name:      c.mdbName,
					Namespace: c.mdbNamespace,
				},
				Spec: mdbv1.MongoDbSpec{
					DbCommonSpec: mdbv1.DbCommonSpec{
						ClusterDomain:   c.clusterDomain,
						ExternalMembers: c.externalMembers,
					},
					Members: c.members,
				},
			}
			src := NewEnterpriseResourceSearchSource(mdb, c.processToHostnameMap)
			seeds := src.HostSeeds()

			assert.Equal(t, c.expectedSeeds, seeds)
		})
	}
}
