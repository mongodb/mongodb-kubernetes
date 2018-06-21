package om

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCreateMongodProcess(t *testing.T) {
	process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", "3.6.4")

	assert.Equal(t, "trinity", process.Name())
	assert.Equal(t, "trinity-0.trinity-svc.svc.cluster.local", process.HostName())
	assert.Equal(t, "3.6.4", process.Version())
	assert.Equal(t, "/data", process.DbPath())
	assert.Equal(t, "/data/mongodb.log", process.LogPath())
	assert.Equal(t, 5, process.authSchemaVersion())
	assert.Equal(t, "3.6", process.featureCompatibilityVersion())
	assert.Equal(t, "", process.replicaSetName())
	assert.Equal(t, map[string]interface{}{"port": 27017}, process.Args()["net"])
}

func TestCreateMongodProcess_authSchemaVersion(t *testing.T) {
	process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", "2.6.2")
	assert.Equal(t, 3, process.authSchemaVersion())

	process = NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", "aaaa")
	assert.Equal(t, 5, process.authSchemaVersion())

	process = NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", "4.0.0")
	assert.Equal(t, 5, process.authSchemaVersion())
}

func TestCreateMongodProcess_featureCompatibilityVersion(t *testing.T) {
	process := NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", "3.0.6")
	assert.Equal(t, "", process.featureCompatibilityVersion())

	process = NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", "3.2.0")
	assert.Equal(t, "3.2", process.featureCompatibilityVersion())

	process = NewMongodProcess("trinity", "trinity-0.trinity-svc.svc.cluster.local", "aaa")
	assert.Equal(t, "", process.featureCompatibilityVersion())
}
