package om

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

func TestCreateMongodProcess(t *testing.T) {
	process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", DefaultMongoDBVersioned("4.0.5"))

	assert.Equal(t, "trinity", process.Name())
	assert.Equal(t, "trinity-0.trinity-svc.svc.cluster.local", process.HostName())
	assert.Equal(t, "4.0.5", process.Version())
	assert.Equal(t, "4.0", process.featureCompatibilityVersion())
	assert.Equal(t, "/data", process.DbPath())
	assert.Equal(t, "/var/log/mongodb-mms-automation/mongodb.log", process.LogPath())
	assert.Equal(t, 5, process.authSchemaVersion())
	assert.Equal(t, "", process.replicaSetName())
	assert.Equal(t, map[string]interface{}{"port": util.MongoDbDefaultPort}, process.EnsureNetConfig())
}

func TestCreateMongodProcess_authSchemaVersion(t *testing.T) {
	process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", DefaultMongoDBVersioned("2.6.2"))
	assert.Equal(t, 3, process.authSchemaVersion())

	process = NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", DefaultMongoDBVersioned("aaaa"))
	assert.Equal(t, 5, process.authSchemaVersion())

	process = NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", DefaultMongoDBVersioned("4.0.0"))
	assert.Equal(t, 5, process.authSchemaVersion())
}

func TestCreateMongodProcess_featureCompatibilityVersion(t *testing.T) {
	process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", DefaultMongoDBVersioned("3.0.6"))
	assert.Equal(t, "", process.featureCompatibilityVersion())

	process = NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", DefaultMongoDBVersioned("3.2.0"))
	assert.Equal(t, "3.2", process.featureCompatibilityVersion())

	process = NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", DefaultMongoDBVersioned("aaa"))
	assert.Equal(t, "", process.featureCompatibilityVersion())

	mdb := DefaultMongoDB().SetVersion("4.2.1").SetFCVersion("4.0").Build()
	process = NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", mdb)
	assert.Equal(t, "4.0", process.featureCompatibilityVersion())
}

func TestConfigureSSL_Process(t *testing.T) {
	process := Process{}
	process.configureTLS(&mongodb.NetSpec{SSL: mongodb.SSLSpec{Mode: mongodb.RequireSSLMode}})
	assert.Equal(t, map[string]interface{}{"mode": mongodb.RequireSSLMode, "PEMKeyFile": "/mongodb-automation/server.pem"}, process.SSLConfig())

	process = Process{}
	process.configureTLS(&mongodb.NetSpec{SSL: mongodb.SSLSpec{Mode: ""}})
	assert.Empty(t, process.SSLConfig())

	process = Process{}
	process.configureTLS(&mongodb.NetSpec{})
	assert.Empty(t, process.SSLConfig())
}

func TestConfigureX509_Process(t *testing.T) {
	mdb := &mongodb.MongoDB{
		Spec: mongodb.MongoDbSpec{
			Version:  "3.6.4",
			Security: &mongodb.Security{},
		},
	}
	process := NewMongodProcess(
		"trinity", "trinity-0.trinity-svc.svc.cluster.local", mdb,
	)

	process.ConfigureClusterAuthMode("") // should not update fields
	assert.NotContains(t, process.security(), "clusterAuthMode")
	assert.NotContains(t, process.SSLConfig(), "clusterFile")

	process.ConfigureClusterAuthMode(util.X509) // should update fields if specified as x509
	assert.Equal(t, util.X509, process.security()["clusterAuthMode"])
	assert.Equal(t, fmt.Sprintf("%s%s-pem", util.InternalClusterAuthMountPath, process.Name()), process.SSLConfig()["clusterFile"])
}

func TestCreateMongodProcess_SSL(t *testing.T) {
	additionalConfig := &mongodb.AdditionalMongodConfig{Net: mongodb.NetSpec{SSL: mongodb.SSLSpec{Mode: mongodb.PreferSSLMode}}}
	mdb := DefaultMongoDB().SetVersion("3.6.4").SetFCVersion("3.6").SetAdditionalConfig(additionalConfig).Build()
	process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", mdb)

	assert.Equal(t, map[string]interface{}{"mode": mongodb.PreferSSLMode, "PEMKeyFile": "/mongodb-automation/server.pem"}, process.SSLConfig())
}

func TestCreateMongosProcess_SSL(t *testing.T) {
	additionalConfig := &mongodb.AdditionalMongodConfig{Net: mongodb.NetSpec{SSL: mongodb.SSLSpec{Mode: mongodb.AllowSSLMode}}}
	mdb := DefaultMongoDB().SetVersion("3.6.4").SetFCVersion("3.6").SetAdditionalConfig(additionalConfig).Build()
	process := NewMongosProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", mdb)

	assert.Equal(t, map[string]interface{}{"mode": mongodb.AllowSSLMode, "PEMKeyFile": "/mongodb-automation/server.pem"}, process.SSLConfig())
}

// TestMergeMongodProcess_SSL verifies that merging for the process SSL settings keeps the Operator "owned" properties
// and doesn't overwrite the other Ops Manager initiated configuration
func TestMergeMongodProcess_SSL(t *testing.T) {
	additionalConfig := &mongodb.AdditionalMongodConfig{Net: mongodb.NetSpec{SSL: mongodb.SSLSpec{Mode: mongodb.RequireSSLMode}}}
	operatorMdb := DefaultMongoDB().SetVersion("3.6.4").SetFCVersion("3.6").SetAdditionalConfig(additionalConfig).Build()
	omMdb := DefaultMongoDB().SetVersion("3.6.4").SetFCVersion("3.6").SetAdditionalConfig(&mongodb.AdditionalMongodConfig{}).Build()

	operatorProcess := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", operatorMdb)
	omProcess := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", omMdb)
	omProcess.EnsureSSLConfig()["mode"] = "allowSSL"                      // this will be overridden
	omProcess.EnsureSSLConfig()["PEMKeyFile"] = "/var/mongodb/server.pem" // this will be overridden
	omProcess.EnsureSSLConfig()["sslOnNormalPorts"] = "true"              // this will be left as-is
	omProcess.EnsureSSLConfig()["PEMKeyPassword"] = "qwerty"              // this will be left as-is

	omProcess.mergeFrom(operatorProcess)

	expectedSSLConfig := map[string]interface{}{
		"mode":             mongodb.RequireSSLMode,
		"PEMKeyFile":       "/mongodb-automation/server.pem",
		"sslOnNormalPorts": "true",
		"PEMKeyPassword":   "qwerty",
	}
	assert.Equal(t, expectedSSLConfig, readMapValueAsInterface(omProcess, "args2_6", "net", "ssl"))
}
