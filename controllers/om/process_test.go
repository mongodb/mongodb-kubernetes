package om

import (
	"fmt"
	"testing"

	"github.com/mongodb/mongodb-kubernetes-operator/controllers/construct"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/stretchr/testify/assert"

	"github.com/10gen/ops-manager-kubernetes/pkg/tls"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/maputil"
)

func TestCreateMongodProcess(t *testing.T) {
	t.Run("Create AgentLoggingMongodConfig", func(t *testing.T) {
		spec := defaultMongoDBVersioned("4.0.5")
		process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", spec.GetAdditionalMongodConfig(), spec, "", nil, "4.0")

		assert.Equal(t, "trinity", process.Name())
		assert.Equal(t, "trinity-0.trinity-svc.svc.cluster.local", process.HostName())
		assert.Equal(t, "4.0.5", process.Version())
		assert.Equal(t, "4.0", process.FeatureCompatibilityVersion())
		assert.Equal(t, "/data", process.DbPath())
		assert.Equal(t, "/var/log/mongodb-mms-automation/mongodb.log", process.LogPath())
		assert.Equal(t, 5, process.authSchemaVersion())
		assert.Equal(t, "", process.replicaSetName())
		assert.Equal(t, nil, process.LogRotateSizeThresholdMB())

		expectedMap := map[string]interface{}{"port": int32(util.MongoDbDefaultPort), "tls": map[string]interface{}{
			"mode": "disabled",
		}}
		assert.Equal(t, expectedMap, process.EnsureNetConfig())
	})
	t.Run("Create with Mongodb options", func(t *testing.T) {
		config := mdbv1.NewAdditionalMongodConfig("storage.engine", "inMemory").
			AddOption("setParameter.connPoolMaxConnsPerHost", 500).
			AddOption("storage.dbPath", "/some/other/data") // this will be overridden
		rs := mdbv1.NewReplicaSetBuilder().SetAdditionalConfig(config).Build()

		process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", rs.Spec.AdditionalMongodConfig, rs.GetSpec(), "", nil, "")

		assert.Equal(t, "inMemory", maputil.ReadMapValueAsInterface(process.Args(), "storage", "engine"))
		assert.Equal(t, 500, maputil.ReadMapValueAsInterface(process.Args(), "setParameter", "connPoolMaxConnsPerHost"))
		assert.Equal(t, "/data", process.DbPath())
	})
}

func TestCreateMongodProcessStatic(t *testing.T) {
	t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))
	t.Setenv(construct.MongodbImageEnv, "mongodb/mongodb-enterprise-server")
	t.Run("Create AgentLoggingMongodConfig", func(t *testing.T) {
		spec := defaultMongoDBVersioned("4.0.5")
		process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", spec.GetAdditionalMongodConfig(), spec, "", map[string]string{}, "4.0")

		assert.Equal(t, "trinity", process.Name())
		assert.Equal(t, "trinity-0.trinity-svc.svc.cluster.local", process.HostName())
		assert.Equal(t, "4.0.5-ent", process.Version())
		assert.Equal(t, "4.0", process.FeatureCompatibilityVersion())
		assert.Equal(t, "/data", process.DbPath())
		assert.Equal(t, "/var/log/mongodb-mms-automation/mongodb.log", process.LogPath())
		assert.Equal(t, 5, process.authSchemaVersion())
		assert.Equal(t, "", process.replicaSetName())

		expectedMap := map[string]interface{}{"port": int32(util.MongoDbDefaultPort), "tls": map[string]interface{}{
			"mode": "disabled",
		}}
		assert.Equal(t, expectedMap, process.EnsureNetConfig())
	})
	t.Run("Create with Mongodb options", func(t *testing.T) {
		config := mdbv1.NewAdditionalMongodConfig("storage.engine", "inMemory").
			AddOption("setParameter.connPoolMaxConnsPerHost", 500).
			AddOption("storage.dbPath", "/some/other/data") // this will be overridden
		rs := mdbv1.NewReplicaSetBuilder().SetAdditionalConfig(config).Build()

		process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", rs.Spec.AdditionalMongodConfig, rs.GetSpec(), "", nil, "")

		assert.Equal(t, "inMemory", maputil.ReadMapValueAsInterface(process.Args(), "storage", "engine"))
		assert.Equal(t, 500, maputil.ReadMapValueAsInterface(process.Args(), "setParameter", "connPoolMaxConnsPerHost"))
		assert.Equal(t, "/data", process.DbPath())
	})
}

func TestConfigureSSL_Process(t *testing.T) {
	process := Process{}

	process.ConfigureTLS(tls.Require, "pem-file0")
	assert.Equal(t, map[string]interface{}{"mode": string(tls.Require), "certificateKeyFile": "pem-file0"}, process.TLSConfig())

	process = Process{}
	process.ConfigureTLS("", "pem-file1")
	assert.Equal(t, map[string]interface{}{"mode": "", "certificateKeyFile": "pem-file1"}, process.TLSConfig())

	process = Process{}
	process.ConfigureTLS(tls.Disabled, "pem-file2")
	assert.Equal(t, map[string]interface{}{"mode": string(tls.Disabled)}, process.TLSConfig())
}

func TestConfigureSSL_Process_CertificateKeyFile(t *testing.T) {
	t.Run("When provided with certificateKeyFile attribute name, it is maintained", func(t *testing.T) {
		process := Process{}
		tlsConfig := process.EnsureTLSConfig()
		tlsConfig["certificateKeyFile"] = "xxx"
		process.ConfigureTLS(tls.Require, "pem-file0")
		assert.Equal(t, map[string]interface{}{"mode": string(tls.Require), "certificateKeyFile": "pem-file0"}, process.TLSConfig())
	})

	t.Run("A non-defined mode keeps the certificateKeyFile attribute name", func(t *testing.T) {
		process := Process{}
		tlsConfig := process.EnsureTLSConfig()
		tlsConfig["certificateKeyFile"] = "xxx"
		process.ConfigureTLS("", "pem-file1")
		assert.Equal(t, map[string]interface{}{"mode": "", "certificateKeyFile": "pem-file1"}, process.TLSConfig())
	})

	t.Run("If TLS is disabled, the certificateKeyFile attribute is deleted", func(t *testing.T) {
		process := Process{}
		tlsConfig := process.EnsureTLSConfig()
		tlsConfig["certificateKeyFile"] = "xxx"
		process.ConfigureTLS(tls.Disabled, "pem-file2")
		assert.Equal(t, map[string]interface{}{"mode": string(tls.Disabled)}, process.TLSConfig())
	})
}

func TestTlsConfig(t *testing.T) {
	process := Process{}
	process.ConfigureTLS(tls.Require, "another-pem-file")
	process.Args()["tls"] = map[string]interface{}{
		"mode":       "requireTLS",
		"PEMKeyFile": "another-pem-file",
	}

	tlsConfig := process.TLSConfig()
	assert.NotNil(t, tlsConfig)
	assert.Equal(t, tlsConfig["mode"], "requireTLS")
	assert.Equal(t, tlsConfig["certificateKeyFile"], "another-pem-file")
}

func TestConfigureX509_Process(t *testing.T) {
	mdb := &mdbv1.MongoDB{
		Spec: mdbv1.MongoDbSpec{
			DbCommonSpec: mdbv1.DbCommonSpec{
				Version: "3.6.4",
				Security: &mdbv1.Security{
					Authentication: &mdbv1.Authentication{
						Modes: []mdbv1.AuthMode{util.X509},
					},
				},
			},
		},
	}
	process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", &mdbv1.AdditionalMongodConfig{}, mdb.GetSpec(), "", nil, "")

	process.ConfigureClusterAuthMode("", "") // should not update fields
	assert.NotContains(t, process.security(), "clusterAuthMode")
	assert.NotContains(t, process.TLSConfig(), "clusterFile")

	process.ConfigureClusterAuthMode(util.X509, "") // should update fields if specified as x509
	assert.Equal(t, "x509", process.security()["clusterAuthMode"])
	assert.Equal(t, fmt.Sprintf("%s%s-pem", util.InternalClusterAuthMountPath, process.Name()), process.TLSConfig()["clusterFile"])
}

func TestCreateMongodProcess_SSL(t *testing.T) {
	additionalConfig := mdbv1.NewAdditionalMongodConfig("net.ssl.mode", string(tls.Prefer))

	mdb := mdbv1.NewStandaloneBuilder().SetVersion("3.6.4").SetFCVersion("3.6").SetAdditionalConfig(additionalConfig).Build()
	process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", additionalConfig, mdb.GetSpec(), "", nil, "")
	assert.Equal(t, map[string]interface{}{"mode": string(tls.Disabled)}, process.TLSConfig())

	mdb = mdbv1.NewStandaloneBuilder().SetVersion("3.6.4").SetFCVersion("3.6").SetAdditionalConfig(additionalConfig).
		SetSecurityTLSEnabled().Build()

	process = NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", additionalConfig, mdb.GetSpec(), "", nil, "")

	assert.Equal(t, map[string]interface{}{
		"mode":               string(tls.Prefer),
		"certificateKeyFile": "/mongodb-automation/server.pem",
	}, process.TLSConfig())
}

func TestCreateMongosProcess_SSL(t *testing.T) {
	additionalConfig := mdbv1.NewAdditionalMongodConfig("net.ssl.mode", string(tls.Allow))
	mdb := mdbv1.NewStandaloneBuilder().SetVersion("3.6.4").SetFCVersion("3.6").SetAdditionalConfig(additionalConfig).
		SetSecurityTLSEnabled().Build()
	process := NewMongosProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", additionalConfig, mdb.GetSpec(), "", nil, "")

	assert.Equal(t, map[string]interface{}{"mode": string(tls.Allow), "certificateKeyFile": "/mongodb-automation/server.pem"}, process.TLSConfig())
}

func TestCreateMongodMongosProcess_TLSModeForDifferentSpecs(t *testing.T) {
	assertTLSConfig := func(p Process) {
		expectedMap := map[string]interface{}{
			"mode":               string(tls.Allow),
			"certificateKeyFile": "/mongodb-automation/server.pem",
		}
		assert.Equal(t, expectedMap, p.TLSConfig())
	}

	getSpec := func(builder *mdbv1.MongoDBBuilder) mdbv1.DbSpec {
		return builder.SetSecurityTLSEnabled().Build().GetSpec()
	}

	name := "name"
	host := "host"
	additionalConfig := mdbv1.NewAdditionalMongodConfig("net.tls.mode", string(tls.Allow))

	// standalone spec
	assertTLSConfig(NewMongodProcess(name, host, additionalConfig, getSpec(mdbv1.NewStandaloneBuilder()), "", nil, ""))

	// replica set spec
	assertTLSConfig(NewMongodProcess(name, host, additionalConfig, getSpec(mdbv1.NewReplicaSetBuilder()), "", nil, ""))

	// sharded cluster spec
	assertTLSConfig(NewMongosProcess(name, host, additionalConfig, getSpec(mdbv1.NewClusterBuilder()), "", nil, ""))
	assertTLSConfig(NewMongodProcess(name, host, additionalConfig, getSpec(mdbv1.NewClusterBuilder()), "", nil, ""))
}

// TestMergeMongodProcess_SSL verifies that merging for the process SSL settings keeps the Operator "owned" properties
// and doesn't overwrite the other Ops Manager initiated configuration
func TestMergeMongodProcess_SSL(t *testing.T) {
	additionalConfig := mdbv1.NewAdditionalMongodConfig("net.ssl.mode", string(tls.Require))
	operatorMdb := mdbv1.NewStandaloneBuilder().SetVersion("3.6.4").SetFCVersion("3.6").
		SetAdditionalConfig(additionalConfig).SetSecurityTLSEnabled().Build()

	omMdb := mdbv1.NewStandaloneBuilder().SetVersion("3.6.4").SetFCVersion("3.6").
		SetAdditionalConfig(mdbv1.NewEmptyAdditionalMongodConfig()).Build()

	operatorProcess := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", &mdbv1.AdditionalMongodConfig{}, operatorMdb.GetSpec(), "", nil, "")
	omProcess := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", &mdbv1.AdditionalMongodConfig{}, omMdb.GetSpec(), "", nil, "")
	omProcess.EnsureTLSConfig()["mode"] = "allowTLS"                      // this will be overridden
	omProcess.EnsureTLSConfig()["PEMKeyFile"] = "/var/mongodb/server.pem" // this will be overridden
	omProcess.EnsureTLSConfig()["sslOnNormalPorts"] = "true"              // this will be left as-is
	omProcess.EnsureTLSConfig()["PEMKeyPassword"] = "qwerty"              // this will be left as-is

	omProcess.mergeFrom(operatorProcess, nil, nil)

	expectedSSLConfig := map[string]interface{}{
		"mode":               string(tls.Require),
		"certificateKeyFile": "/mongodb-automation/server.pem",
		"sslOnNormalPorts":   "true",
		"PEMKeyPassword":     "qwerty",
	}
	assert.Equal(t, expectedSSLConfig, maputil.ReadMapValueAsInterface(omProcess, "args2_6", "net", "tls"))
}

func TestMergeMongodProcess_MongodbOptions(t *testing.T) {
	omMdb := mdbv1.NewStandaloneBuilder().SetAdditionalConfig(
		mdbv1.NewAdditionalMongodConfig("storage.wiredTiger.engineConfig.cacheSizeGB", 3)).Build()
	omProcess := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", omMdb.Spec.AdditionalMongodConfig, omMdb.GetSpec(), "", nil, "")

	operatorMdb := mdbv1.NewStandaloneBuilder().SetAdditionalConfig(
		mdbv1.NewAdditionalMongodConfig("storage.wiredTiger.engineConfig.directoryForIndexes", "/some/dir")).Build()
	operatorProcess := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", operatorMdb.Spec.AdditionalMongodConfig, operatorMdb.GetSpec(), "", nil, "")

	omProcess.mergeFrom(operatorProcess, nil, nil)

	expectedArgs := map[string]interface{}{
		"net": map[string]interface{}{
			"port": int32(27017),
			"tls": map[string]interface{}{
				"mode": "disabled",
			},
		},
		"storage": map[string]interface{}{
			"dbPath": "/data",
			"wiredTiger": map[string]interface{}{
				"engineConfig": map[string]interface{}{
					"cacheSizeGB":         3,           // This is the native OM configuration
					"directoryForIndexes": "/some/dir", // This is the configuration set by MongoDB spec
				},
			},
		},
		"systemLog": map[string]interface{}{
			"destination": "file",
			"path":        "/var/log/mongodb-mms-automation/mongodb.log",
		},
	}

	assert.Equal(t, expectedArgs, omProcess.Args())
}

func TestMergeMongodProcess_AdditionalMongodConfig_CanBeRemoved(t *testing.T) {
	prevAdditionalConfig := mdbv1.NewEmptyAdditionalMongodConfig()
	prevAdditionalConfig.AddOption("storage.wiredTiger.engineConfig.cacheSizeGB", 3)
	prevAdditionalConfig.AddOption("some.other.option", "value")
	prevAdditionalConfig.AddOption("some.other.option2", "value2")

	omMdb := mdbv1.NewStandaloneBuilder().SetAdditionalConfig(prevAdditionalConfig).Build()
	omProcess := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", omMdb.Spec.AdditionalMongodConfig, omMdb.GetSpec(), "", nil, "")

	specAdditionalConfig := mdbv1.NewEmptyAdditionalMongodConfig()
	// we are changing the cacheSize to 4
	specAdditionalConfig.AddOption("storage.wiredTiger.engineConfig.cacheSizeGB", 4)
	// here we are simulating removing "some.other.option2" by not specifying it.
	specAdditionalConfig.AddOption("some.other.option", "value")

	operatorMdb := mdbv1.NewStandaloneBuilder().SetAdditionalConfig(specAdditionalConfig).Build()
	operatorProcess := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", operatorMdb.Spec.AdditionalMongodConfig, operatorMdb.GetSpec(), "", nil, "")

	omProcess.mergeFrom(operatorProcess, specAdditionalConfig.ToMap(), prevAdditionalConfig.ToMap())

	args := omProcess.Args()

	expectedArgs := map[string]interface{}{
		"net": map[string]interface{}{
			"port": int32(27017),
			"tls": map[string]interface{}{
				"mode": "disabled",
			},
		},
		"storage": map[string]interface{}{
			"dbPath": "/data",
			"wiredTiger": map[string]interface{}{
				"engineConfig": map[string]interface{}{
					"cacheSizeGB": 4,
				},
			},
		},
		"systemLog": map[string]interface{}{
			"destination": "file",
			"path":        "/var/log/mongodb-mms-automation/mongodb.log",
		},
		"some": map[string]interface{}{
			"other": map[string]interface{}{
				"option": "value",
			},
		},
	}

	assert.Equal(t, expectedArgs, args, "option2 should have been removed as it was not specified")
}
