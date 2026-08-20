package process

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
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
			architectures.NonStatic,
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
			architectures.NonStatic,
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
			architectures.NonStatic,
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
			architectures.NonStatic,
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
			architectures.NonStatic,
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
		architectures.NonStatic,
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

func TestCreateMongodProcessesMultiFromCounts(t *testing.T) {
	clusters := []string{"cluster-0", "cluster-1", "cluster-2"}
	mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetClusterSpecList(clusters).Build()

	t.Run("Explicit counts drive names and hostnames", func(t *testing.T) {
		counts := []ClusterProcessCount{
			{ClusterName: "cluster-0", ClusterIndex: 0, MemberCount: 2},
			{ClusterName: "cluster-1", ClusterIndex: 1, MemberCount: 1},
		}
		processes := CreateMongodProcessesMultiFromCounts(defaultMongoDBImage, false, *mrs, counts, "", architectures.NonStatic)

		require.Len(t, processes, 3)
		expectedNames := []string{
			fmt.Sprintf("%s-0-0", mrs.Name),
			fmt.Sprintf("%s-0-1", mrs.Name),
			fmt.Sprintf("%s-1-0", mrs.Name),
		}
		var expectedHostnames []string
		for _, count := range counts {
			expectedHostnames = append(expectedHostnames, dns.GetMultiClusterProcessHostnames(mrs.Name, mrs.Namespace, count.ClusterIndex, count.MemberCount, mrs.Spec.GetClusterDomain(), nil)...)
		}
		for i, process := range processes {
			assert.Equal(t, expectedNames[i], process.Name())
			assert.Equal(t, expectedHostnames[i], process.HostName())
		}
	})

	t.Run("Zero-count cluster contributes no processes", func(t *testing.T) {
		counts := []ClusterProcessCount{
			{ClusterName: "cluster-0", ClusterIndex: 0, MemberCount: 0},
			{ClusterName: "cluster-1", ClusterIndex: 1, MemberCount: 1},
		}
		processes := CreateMongodProcessesMultiFromCounts(defaultMongoDBImage, false, *mrs, counts, "", architectures.NonStatic)

		require.Len(t, processes, 1)
		assert.Equal(t, fmt.Sprintf("%s-1-0", mrs.Name), processes[0].Name())
	})

	t.Run("Member options line up with the granted process order", func(t *testing.T) {
		priority := func(p string) *string { return &p }
		withOptions := mrs.DeepCopy()
		withOptions.Spec.ClusterSpecList[0].MemberConfig = []automationconfig.MemberOptions{{Priority: priority("2.0")}, {Priority: priority("1.5")}}
		withOptions.Spec.ClusterSpecList[1].MemberConfig = []automationconfig.MemberOptions{{Priority: priority("1.0")}}

		// cluster-0 granted below its configured options, cluster-1 granted above them
		counts := []ClusterProcessCount{
			{ClusterName: "cluster-0", ClusterIndex: 0, MemberCount: 1},
			{ClusterName: "cluster-1", ClusterIndex: 1, MemberCount: 2},
		}
		options := MemberOptionsFromCounts(*withOptions, counts)

		require.Len(t, options, len(CreateMongodProcessesMultiFromCounts(defaultMongoDBImage, false, *withOptions, counts, "", architectures.NonStatic)))
		assert.Equal(t, "2.0", *options[0].Priority, "cluster-0's first member keeps its option, the truncated second never appears")
		assert.Equal(t, "1.0", *options[1].Priority, "cluster-1's first member follows immediately, no positional drift")
		assert.Nil(t, options[2].Priority, "the extra granted member pads with the zero value")
	})

	t.Run("Counts equal to the spec match the legacy builder", func(t *testing.T) {
		counts := make([]ClusterProcessCount, len(mrs.Spec.ClusterSpecList))
		for i, item := range mrs.Spec.ClusterSpecList {
			counts[i] = ClusterProcessCount{ClusterName: item.ClusterName, ClusterIndex: i, MemberCount: item.Members}
		}
		processes := CreateMongodProcessesMultiFromCounts(defaultMongoDBImage, false, *mrs, counts, "", architectures.NonStatic)

		legacyProcesses, err := CreateMongodProcessesWithLimitMulti(defaultMongoDBImage, false, *mrs, "", architectures.NonStatic)
		require.NoError(t, err)
		assert.Equal(t, legacyProcesses, processes)
	})
}
