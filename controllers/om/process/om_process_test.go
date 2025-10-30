package process

import (
	"fmt"
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
	t.Run("Happy path - creates processes with correct integration", func(t *testing.T) {
		mdb := baseReplicaSet("test-rs", 3)
		processes := CreateMongodProcessesFromMongoDB(
			defaultMongoDBImage,
			false,
			mdb,
			3,
			defaultFCV,
			"",
		)

		assert.Len(t, processes, 3, "Should create 3 processes")

		// Verify basic integration - processes are created with correct names and FCV
		for i, process := range processes {
			expectedName := fmt.Sprintf("test-rs-%d", i)
			assert.Equal(t, expectedName, process.Name(), "Process name should be generated correctly")
			assert.Equal(t, defaultFCV, process.FeatureCompatibilityVersion(), "FCV should be set correctly")
			assert.NotEmpty(t, process.HostName(), "Hostname should be generated")
		}
	})

	t.Run("Limit parameter controls process count", func(t *testing.T) {
		mdb := baseReplicaSet("scale-rs", 5)

		// Test limit less than members (scale up in progress)
		processesScaleUp := CreateMongodProcessesFromMongoDB(
			defaultMongoDBImage,
			false,
			mdb,
			3, // limit
			defaultFCV,
			"",
		)
		assert.Len(t, processesScaleUp, 3, "Limit should control process count during scale up")

		// Test limit greater than members (scale down in progress)
		processesScaleDown := CreateMongodProcessesFromMongoDB(
			defaultMongoDBImage,
			false,
			mdb,
			7, // limit
			defaultFCV,
			"",
		)
		assert.Len(t, processesScaleDown, 7, "Limit should control process count during scale down")

		// Test limit zero
		processesZero := CreateMongodProcessesFromMongoDB(
			defaultMongoDBImage,
			false,
			mdb,
			0, // limit
			defaultFCV,
			"",
		)
		assert.Empty(t, processesZero, "Zero limit should create empty process slice")
	})

	t.Run("TLS cert path flows through to processes", func(t *testing.T) {
		mdb := baseReplicaSet("tls-rs", 2)
		mdb.Spec.Security = &mdbv1.Security{
			TLSConfig: &mdbv1.TLSConfig{Enabled: true},
		}

		tlsCertPath := "/custom/path/to/cert.pem"
		processes := CreateMongodProcessesFromMongoDB(
			defaultMongoDBImage,
			false,
			mdb,
			2,
			defaultFCV,
			tlsCertPath,
		)

		assert.Len(t, processes, 2)

		// Verify TLS configuration is properly integrated
		for i, process := range processes {
			tlsConfig := process.TLSConfig()
			assert.NotNil(t, tlsConfig, "TLS config should be set when cert path provided")
			assert.Equal(t, tlsCertPath, tlsConfig["certificateKeyFile"], "TLS cert path should match at index %d", i)
		}
	})
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
