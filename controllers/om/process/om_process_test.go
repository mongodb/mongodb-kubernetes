package process

import (
	"testing"

	"github.com/stretchr/testify/assert"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/maputil"
)

const (
	defaultMongoDBImage = "mongodb/mongodb-enterprise-server:7.0.0"
	defaultFCV          = "7.0"
	defaultNamespace    = "test-namespace"
)

func TestCreateMongodProcessesFromMongoDB(t *testing.T) {
	tests := []struct {
		name              string
		mdb               *mdbv1.MongoDB
		limit             int
		mongoDBImage      string
		forceEnterprise   bool
		fcv               string
		tlsCertPath       string
		expectedCount     int
		expectedHostnames []string
		expectedNames     []string
	}{
		{
			name:            "3-member replica set",
			mdb:             baseReplicaSet("test-rs", 3),
			limit:           3,
			mongoDBImage:    defaultMongoDBImage,
			forceEnterprise: false,
			fcv:             defaultFCV,
			expectedCount:   3,
			expectedHostnames: []string{
				"test-rs-0.test-rs-svc.test-namespace.svc.cluster.local",
				"test-rs-1.test-rs-svc.test-namespace.svc.cluster.local",
				"test-rs-2.test-rs-svc.test-namespace.svc.cluster.local",
			},
			expectedNames: []string{"test-rs-0", "test-rs-1", "test-rs-2"},
		},
		{
			name:            "Single member replica set",
			mdb:             baseReplicaSet("single-rs", 1),
			limit:           1,
			mongoDBImage:    defaultMongoDBImage,
			forceEnterprise: false,
			fcv:             defaultFCV,
			expectedCount:   1,
			expectedHostnames: []string{
				"single-rs-0.single-rs-svc.test-namespace.svc.cluster.local",
			},
			expectedNames: []string{"single-rs-0"},
		},
		{
			name:            "Limit less than members (scale up in progress)",
			mdb:             baseReplicaSet("scale-up-rs", 5),
			limit:           3,
			mongoDBImage:    defaultMongoDBImage,
			forceEnterprise: false,
			fcv:             defaultFCV,
			expectedCount:   3,
			expectedHostnames: []string{
				"scale-up-rs-0.scale-up-rs-svc.test-namespace.svc.cluster.local",
				"scale-up-rs-1.scale-up-rs-svc.test-namespace.svc.cluster.local",
				"scale-up-rs-2.scale-up-rs-svc.test-namespace.svc.cluster.local",
			},
			expectedNames: []string{"scale-up-rs-0", "scale-up-rs-1", "scale-up-rs-2"},
		},
		{
			name:            "Limit greater than members (scale down in progress)",
			mdb:             baseReplicaSet("scale-down-rs", 3),
			limit:           5,
			mongoDBImage:    defaultMongoDBImage,
			forceEnterprise: false,
			fcv:             defaultFCV,
			expectedCount:   5,
			expectedHostnames: []string{
				"scale-down-rs-0.scale-down-rs-svc.test-namespace.svc.cluster.local",
				"scale-down-rs-1.scale-down-rs-svc.test-namespace.svc.cluster.local",
				"scale-down-rs-2.scale-down-rs-svc.test-namespace.svc.cluster.local",
				"scale-down-rs-3.scale-down-rs-svc.test-namespace.svc.cluster.local",
				"scale-down-rs-4.scale-down-rs-svc.test-namespace.svc.cluster.local",
			},
			expectedNames: []string{"scale-down-rs-0", "scale-down-rs-1", "scale-down-rs-2", "scale-down-rs-3", "scale-down-rs-4"},
		},
		{
			name:              "Limit zero creates empty slice",
			mdb:               baseReplicaSet("empty-rs", 3),
			limit:             0,
			mongoDBImage:      defaultMongoDBImage,
			forceEnterprise:   false,
			fcv:               defaultFCV,
			expectedCount:     0,
			expectedHostnames: []string{},
			expectedNames:     []string{},
		},
		{
			name: "Custom cluster domain",
			mdb: func() *mdbv1.MongoDB {
				rs := baseReplicaSet("custom-domain-rs", 2)
				rs.Spec.ClusterDomain = "my-cluster.local"
				return rs
			}(),

			limit:           2,
			mongoDBImage:    defaultMongoDBImage,
			forceEnterprise: false,
			fcv:             defaultFCV,
			expectedCount:   2,
			expectedHostnames: []string{
				"custom-domain-rs-0.custom-domain-rs-svc.test-namespace.svc.my-cluster.local",
				"custom-domain-rs-1.custom-domain-rs-svc.test-namespace.svc.my-cluster.local",
			},
			expectedNames: []string{"custom-domain-rs-0", "custom-domain-rs-1"},
		},
		{
			name: "Different namespace",
			mdb: func() *mdbv1.MongoDB {
				rs := baseReplicaSet("other-ns-rs", 2)
				rs.Namespace = "production"
				return rs
			}(),
			limit:           2,
			mongoDBImage:    defaultMongoDBImage,
			forceEnterprise: false,
			fcv:             defaultFCV,
			expectedCount:   2,
			expectedHostnames: []string{
				"other-ns-rs-0.other-ns-rs-svc.production.svc.cluster.local",
				"other-ns-rs-1.other-ns-rs-svc.production.svc.cluster.local",
			},
			expectedNames: []string{"other-ns-rs-0", "other-ns-rs-1"},
		},
		{
			name: "With TLS cert path",
			mdb: func() *mdbv1.MongoDB {
				rs := baseReplicaSet("tls-rs", 2)
				rs.Spec.Security = &mdbv1.Security{
					TLSConfig: &mdbv1.TLSConfig{Enabled: true},
				}
				return rs
			}(),
			limit:           2,
			mongoDBImage:    defaultMongoDBImage,
			forceEnterprise: false,
			fcv:             defaultFCV,
			tlsCertPath:     "/path/to/cert.pem",
			expectedCount:   2,
			expectedHostnames: []string{
				"tls-rs-0.tls-rs-svc.test-namespace.svc.cluster.local",
				"tls-rs-1.tls-rs-svc.test-namespace.svc.cluster.local",
			},
			expectedNames: []string{"tls-rs-0", "tls-rs-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processes := CreateMongodProcessesFromMongoDB(
				tt.mongoDBImage,
				tt.forceEnterprise,
				tt.mdb,
				tt.limit,
				tt.fcv,
				tt.tlsCertPath,
			)

			assert.Equal(t, tt.expectedCount, len(processes), "Process count mismatch")

			for i, process := range processes {
				assert.Equal(t, tt.expectedNames[i], process.Name(), "Process name mismatch at index %d", i)
				assert.Equal(t, tt.expectedHostnames[i], process.HostName(), "Hostname mismatch at index %d", i)
				assert.Equal(t, tt.fcv, process.FeatureCompatibilityVersion(), "FCV mismatch at index %d", i)

				if tt.tlsCertPath != "" {
					tlsConfig := process.TLSConfig()
					assert.NotNil(t, tlsConfig, "TLS config should not be nil when cert path is provided")
					assert.Equal(t, tt.tlsCertPath, tlsConfig["certificateKeyFile"], "TLS cert path mismatch at index %d", i)
				}
			}
		})
	}
}

func TestCreateMongodProcessesFromMongoDB_AdditionalConfig(t *testing.T) {
	config := mdbv1.NewAdditionalMongodConfig("storage.engine", "inMemory").
		AddOption("replication.oplogSizeMB", 2048)

	mdb := mdbv1.NewReplicaSetBuilder().
		SetName("config-rs").
		SetNamespace(defaultNamespace).
		SetMembers(2).
		SetVersion("7.0.0").
		SetFCVersion(defaultFCV).
		SetAdditionalConfig(config).
		Build()

	processes := CreateMongodProcessesFromMongoDB(
		defaultMongoDBImage,
		false,
		mdb,
		2,
		defaultFCV,
		"",
	)

	assert.Len(t, processes, 2)

	for i, process := range processes {
		assert.Equal(t, "inMemory", maputil.ReadMapValueAsInterface(process.Args(), "storage", "engine"),
			"Storage engine mismatch at index %d", i)
		assert.Equal(t, 2048, maputil.ReadMapValueAsInterface(process.Args(), "replication", "oplogSizeMB"),
			"OplogSizeMB mismatch at index %d", i)
	}
}

func baseReplicaSet(name string, members int) *mdbv1.MongoDB {
	return mdbv1.NewReplicaSetBuilder().
		SetName(name).
		SetNamespace(defaultNamespace).
		SetMembers(members).
		SetVersion("7.0.0").
		SetFCVersion(defaultFCV).
		Build()
}
