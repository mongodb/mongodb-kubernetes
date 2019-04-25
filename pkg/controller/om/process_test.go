package om

import (
	"testing"

	"github.com/stretchr/testify/assert"

	mongodb "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

func TestCreateMongodProcess(t *testing.T) {
	process := NewMongodProcess(
		"trinity", "trinity-0.trinity-svc.svc.cluster.local", "3.6.4", &mongodb.AdditionalMongodConfig{},
	)

	assert.Equal(t, "trinity", process.Name())
	assert.Equal(t, "trinity-0.trinity-svc.svc.cluster.local", process.HostName())
	assert.Equal(t, "3.6.4", process.Version())
	assert.Equal(t, "/data", process.DbPath())
	assert.Equal(t, "/var/log/mongodb-mms-automation/mongodb.log", process.LogPath())
	assert.Equal(t, 5, process.authSchemaVersion())
	assert.Equal(t, "3.6", process.featureCompatibilityVersion())
	assert.Equal(t, "", process.replicaSetName())
	assert.Equal(t, map[string]interface{}{"port": util.MongoDbDefaultPort}, process.EnsureNetConfig())
}

func TestCreateMongodProcess_authSchemaVersion(t *testing.T) {
	additionalConfig := &mongodb.AdditionalMongodConfig{}
	process := NewMongodProcess(
		"trinity", "trinity-0.trinity-svc.svc.cluster.local", "2.6.2", additionalConfig,
	)
	assert.Equal(t, 3, process.authSchemaVersion())

	process = NewMongodProcess(
		"trinity", "trinity-0.trinity-svc.svc.cluster.local", "aaaa", additionalConfig,
	)
	assert.Equal(t, 5, process.authSchemaVersion())

	process = NewMongodProcess(
		"trinity", "trinity-0.trinity-svc.svc.cluster.local", "4.0.0", additionalConfig,
	)
	assert.Equal(t, 5, process.authSchemaVersion())
}

func TestCreateMongodProcess_featureCompatibilityVersion(t *testing.T) {
	additionalConfig := &mongodb.AdditionalMongodConfig{}
	process := NewMongodProcess(
		"trinity", "trinity-0.trinity-svc.svc.cluster.local", "3.0.6", additionalConfig,
	)
	assert.Equal(t, "", process.featureCompatibilityVersion())

	process = NewMongodProcess(
		"trinity", "trinity-0.trinity-svc.svc.cluster.local", "3.2.0", additionalConfig,
	)
	assert.Equal(t, "3.2", process.featureCompatibilityVersion())

	process = NewMongodProcess(
		"trinity", "trinity-0.trinity-svc.svc.cluster.local", "aaa", additionalConfig,
	)
	assert.Equal(t, "", process.featureCompatibilityVersion())
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

func TestCreateMongodProcess_SSL(t *testing.T) {
	additionalConfig := &mongodb.AdditionalMongodConfig{Net: mongodb.NetSpec{SSL: mongodb.SSLSpec{Mode: mongodb.PreferSSLMode}}}
	process := NewMongodProcess(
		"trinity", "trinity-0.trinity-svc.svc.cluster.local", "3.6.4", additionalConfig,
	)

	assert.Equal(t, map[string]interface{}{"mode": mongodb.PreferSSLMode, "PEMKeyFile": "/mongodb-automation/server.pem"}, process.SSLConfig())
}

func TestCreateMongosProcess_SSL(t *testing.T) {
	additionalConfig := &mongodb.AdditionalMongodConfig{Net: mongodb.NetSpec{SSL: mongodb.SSLSpec{Mode: mongodb.AllowSSLMode}}}
	process := NewMongosProcess(
		"trinity", "trinity-0.trinity-svc.svc.cluster.local", "3.6.4", additionalConfig,
	)
	assert.Equal(t, map[string]interface{}{"mode": mongodb.AllowSSLMode, "PEMKeyFile": "/mongodb-automation/server.pem"}, process.SSLConfig())
}

// TestMergeMongodProcess_SSL verifies that merging for the process SSL settings keeps the Operator "owned" properties
// and doesn't overwrite the other Ops Manager initiated configuration
func TestMergeMongodProcess_SSL(t *testing.T) {
	additionalConfig := &mongodb.AdditionalMongodConfig{Net: mongodb.NetSpec{SSL: mongodb.SSLSpec{Mode: mongodb.RequireSSLMode}}}
	operatorProcess := NewMongodProcess(
		"trinity", "trinity-0.trinity-svc.svc.cluster.local", "3.6.4", additionalConfig,
	)
	omProcess := NewMongodProcess(
		"trinity", "trinity-0.trinity-svc.svc.cluster.local", "3.6.4", &mongodb.AdditionalMongodConfig{},
	)
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
