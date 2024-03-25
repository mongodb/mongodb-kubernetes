package om

import (
	"testing"

	"github.com/mongodb/mongodb-kubernetes-operator/controllers/construct"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
)

func defaultMongoDBAppDBVersioned(version string) *omv1.AppDBSpec {
	appdb := *omv1.DefaultAppDbBuilder().Build()
	appdb.Version = version

	return &appdb
}

func TestCreateMongodProcessAppDB(t *testing.T) {
	process := NewMongodProcessAppDB("trinity", "trinity-0.trinity-svc.svc.cluster.local", defaultMongoDBAppDBVersioned("4.0.5"))

	assert.Equal(t, "trinity", process.Name())
	assert.Equal(t, "trinity-0.trinity-svc.svc.cluster.local", process.HostName())
	assert.Equal(t, "4.0.5", process.Version())
	assert.Equal(t, "4.0", process.FeatureCompatibilityVersion())
	assert.Equal(t, "/data", process.DbPath())
	assert.Equal(t, "/var/log/mongodb-mms-automation/mongodb.log", process.LogPath())
	assert.Equal(t, 5, process.authSchemaVersion())
	assert.Equal(t, "", process.replicaSetName())

	expectedMap := map[string]interface{}{"port": int32(util.MongoDbDefaultPort)}
	assert.Equal(t, expectedMap, process.EnsureNetConfig())
}

func TestCreateMongodProcessAppDBStatic(t *testing.T) {
	t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))
	t.Setenv(construct.MongodbImageEnv, "mongodb/mongodb-community-server")

	process := NewMongodProcessAppDB("trinity", "trinity-0.trinity-svc.svc.cluster.local", defaultMongoDBAppDBVersioned("4.0.5"))
	assert.Equal(t, "trinity", process.Name())
	assert.Equal(t, "trinity-0.trinity-svc.svc.cluster.local", process.HostName())
	assert.Equal(t, "4.0.5", process.Version())
	assert.Equal(t, "4.0", process.FeatureCompatibilityVersion())
	assert.Equal(t, "/data", process.DbPath())
	assert.Equal(t, "/var/log/mongodb-mms-automation/mongodb.log", process.LogPath())
	assert.Equal(t, 5, process.authSchemaVersion())
	assert.Equal(t, "", process.replicaSetName())

	expectedMap := map[string]interface{}{"port": int32(util.MongoDbDefaultPort)}
	assert.Equal(t, expectedMap, process.EnsureNetConfig())
}

func TestCreateProcessWithNoTLSEnabled(t *testing.T) {
	process := NewMongodProcessAppDB("no-tls-process-0", "no-tls-process-0.cluster.local", defaultMongoDBAppDBVersioned("4.0.5"))

	args := process["args2_6"].(map[string]interface{})
	net := args["net"].(map[string]interface{})
	assert.Nil(t, net["ssl"])
}

func TestCreateProcessWithTLSEnabled(t *testing.T) {
	appdb := defaultMongoDBAppDBVersioned("4.0.5")
	appdb.Security = &mdbv1.Security{
		TLSConfig: &mdbv1.TLSConfig{Enabled: true},
	}

	process := NewMongodProcessAppDB("tls-process-1", "tls-process-1.cluster.local", appdb)

	pemKeyFile := "/var/lib/mongodb-automation/secrets/certs/tls-process-1-pem"
	expectedMap := map[string]interface{}{"port": int32(27017), "tls": map[string]interface{}{"mode": "requireTLS", "certificateKeyFile": pemKeyFile}}
	args := process["args2_6"].(map[string]interface{})
	net := args["net"]
	assert.Equal(t, expectedMap, net)
}
