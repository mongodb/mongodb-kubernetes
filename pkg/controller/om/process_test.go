package om

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

func TestCreateMongodProcess(t *testing.T) {
	process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", defaultMongoDBVersioned("4.0.5"))

	assert.Equal(t, "trinity", process.Name())
	assert.Equal(t, "trinity-0.trinity-svc.svc.cluster.local", process.HostName())
	assert.Equal(t, "4.0.5", process.Version())
	assert.Equal(t, "4.0", process.FeatureCompatibilityVersion())
	assert.Equal(t, "/data", process.DbPath())
	assert.Equal(t, "/var/log/mongodb-mms-automation/mongodb.log", process.LogPath())
	assert.Equal(t, 5, process.authSchemaVersion())
	assert.Equal(t, "", process.replicaSetName())

	expectedMap := map[string]interface{}{"port": util.MongoDbDefaultPort, "ssl": map[string]interface{}{
		"mode": "disabled",
	}}
	assert.Equal(t, expectedMap, process.EnsureNetConfig())
}

func TestCreateMongodProcess_authSchemaVersion(t *testing.T) {
	process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", defaultMongoDBVersioned("2.6.2"))
	assert.Equal(t, 3, process.authSchemaVersion())

	process = NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", defaultMongoDBVersioned("aaaa"))
	assert.Equal(t, 5, process.authSchemaVersion())

	process = NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", defaultMongoDBVersioned("4.0.0"))
	assert.Equal(t, 5, process.authSchemaVersion())
}

func TestCreateMongodProcess_featureCompatibilityVersion(t *testing.T) {
	process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", defaultMongoDBVersioned("3.0.6"))
	assert.Equal(t, "", process.FeatureCompatibilityVersion())

	process = NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", defaultMongoDBVersioned("3.2.0"))
	assert.Equal(t, "", process.FeatureCompatibilityVersion())

	process = NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", defaultMongoDBVersioned("aaa"))
	assert.Equal(t, "", process.FeatureCompatibilityVersion())

	mdb := mdbv1.NewStandaloneBuilder().SetVersion("4.2.1").SetFCVersion("4.0").Build()
	process = NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", mdb)
	assert.Equal(t, "4.0", process.FeatureCompatibilityVersion())
}

func TestConfigureSSL_Process(t *testing.T) {
	process := Process{}

	process.ConfigureTLS(mdbv1.RequireSSLMode, "pem-file0")
	assert.Equal(t, map[string]interface{}{"mode": string(mdbv1.RequireSSLMode), "PEMKeyFile": "pem-file0"}, process.SSLConfig())

	process = Process{}
	process.ConfigureTLS("", "pem-file1")
	assert.Equal(t, map[string]interface{}{"mode": "", "PEMKeyFile": "pem-file1"}, process.SSLConfig())

	process = Process{}
	process.ConfigureTLS(mdbv1.DisabledSSLMode, "pem-file2")
	assert.Equal(t, map[string]interface{}{"mode": string(mdbv1.DisabledSSLMode)}, process.SSLConfig())
}

func TestTlsConfig(t *testing.T) {
	process := Process{}
	process.ConfigureTLS(mdbv1.RequireSSLMode, "another-pem-file")
	process.Args()["tls"] = map[string]interface{}{
		"mode":       "requireSSL",
		"PEMKeyFile": "another-pem-file",
	}

	tlsConfig := process.SSLConfig()
	assert.NotNil(t, tlsConfig)
	assert.Equal(t, tlsConfig["mode"], "requireSSL")
	assert.Equal(t, tlsConfig["PEMKeyFile"], "another-pem-file")
}

func TestConfigureX509_Process(t *testing.T) {
	mdb := &mdbv1.MongoDB{
		Spec: mdbv1.MongoDbSpec{
			Version: "3.6.4",
			Security: &mdbv1.Security{
				Authentication: &mdbv1.Authentication{
					Modes: []string{util.X509},
				},
			},
		},
	}
	process := NewMongodProcess(
		"trinity", "trinity-0.trinity-svc.svc.cluster.local", mdb,
	)

	process.ConfigureClusterAuthMode("") // should not update fields
	assert.NotContains(t, process.security(), "clusterAuthMode")
	assert.NotContains(t, process.SSLConfig(), "clusterFile")

	process.ConfigureClusterAuthMode(util.X509) // should update fields if specified as x509
	assert.Equal(t, "x509", process.security()["clusterAuthMode"])
	assert.Equal(t, fmt.Sprintf("%s%s-pem", util.InternalClusterAuthMountPath, process.Name()), process.SSLConfig()["clusterFile"])
}

func TestCreateMongodProcess_SSL(t *testing.T) {
	additionalConfig := &mdbv1.AdditionalMongodConfig{Net: mdbv1.NetSpec{SSL: mdbv1.SSLSpec{Mode: mdbv1.PreferSSLMode}}}

	mdb := mdbv1.NewStandaloneBuilder().SetVersion("3.6.4").SetFCVersion("3.6").SetAdditionalConfig(additionalConfig).Build()
	process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", mdb)
	assert.Equal(t, map[string]interface{}{"mode": string(mdbv1.DisabledSSLMode)}, process.SSLConfig())

	mdb = mdbv1.NewStandaloneBuilder().SetVersion("3.6.4").SetFCVersion("3.6").SetAdditionalConfig(additionalConfig).
		SetSecurityTLSEnabled().Build()

	process = NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", mdb)

	assert.Equal(t, map[string]interface{}{"mode": string(mdbv1.PreferSSLMode),
		"PEMKeyFile": "/mongodb-automation/server.pem"}, process.SSLConfig())
}

func TestCreateMongosProcess_SSL(t *testing.T) {
	additionalConfig := &mdbv1.AdditionalMongodConfig{Net: mdbv1.NetSpec{SSL: mdbv1.SSLSpec{Mode: mdbv1.AllowSSLMode}}}
	mdb := mdbv1.NewStandaloneBuilder().SetVersion("3.6.4").SetFCVersion("3.6").SetAdditionalConfig(additionalConfig).
		SetSecurityTLSEnabled().Build()
	process := NewMongosProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", mdb)

	assert.Equal(t, map[string]interface{}{"mode": string(mdbv1.AllowSSLMode), "PEMKeyFile": "/mongodb-automation/server.pem"}, process.SSLConfig())
}

// TestMergeMongodProcess_SSL verifies that merging for the process SSL settings keeps the Operator "owned" properties
// and doesn't overwrite the other Ops Manager initiated configuration
func TestMergeMongodProcess_SSL(t *testing.T) {
	additionalConfig := &mdbv1.AdditionalMongodConfig{Net: mdbv1.NetSpec{SSL: mdbv1.SSLSpec{Mode: mdbv1.RequireSSLMode}}}
	operatorMdb := mdbv1.NewStandaloneBuilder().SetVersion("3.6.4").SetFCVersion("3.6").
		SetAdditionalConfig(additionalConfig).SetSecurityTLSEnabled().Build()

	omMdb := mdbv1.NewStandaloneBuilder().SetVersion("3.6.4").SetFCVersion("3.6").
		SetAdditionalConfig(&mdbv1.AdditionalMongodConfig{}).Build()

	operatorProcess := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", operatorMdb)
	omProcess := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", omMdb)
	omProcess.EnsureSSLConfig()["mode"] = "allowSSL"                      // this will be overridden
	omProcess.EnsureSSLConfig()["PEMKeyFile"] = "/var/mongodb/server.pem" // this will be overridden
	omProcess.EnsureSSLConfig()["sslOnNormalPorts"] = "true"              // this will be left as-is
	omProcess.EnsureSSLConfig()["PEMKeyPassword"] = "qwerty"              // this will be left as-is

	omProcess.mergeFrom(operatorProcess)

	expectedSSLConfig := map[string]interface{}{
		"mode":             string(mdbv1.RequireSSLMode),
		"PEMKeyFile":       "/mongodb-automation/server.pem",
		"sslOnNormalPorts": "true",
		"PEMKeyPassword":   "qwerty",
	}
	assert.Equal(t, expectedSSLConfig, readMapValueAsInterface(omProcess, "args2_6", "net", "ssl"))
}
