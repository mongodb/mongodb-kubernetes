package om

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

var testMonitoringConfig = *getTestMonitoringConfig()

func getTestMonitoringConfig() *MonitoringAgentConfig {
	a, _ := BuildMonitoringAgentConfigFromBytes(loadBytesFromTestData("monitoring_config.json"))
	return a
}

func TestMonitoringAgentConfigApply(t *testing.T) {
	config := getTestMonitoringConfig()
	config.MonitoringAgentTemplate.Username = "my-user-name"
	config.MonitoringAgentTemplate.SSLPemKeyFile = util.MergoDelete

	err := config.Apply()
	assert.NoError(t, err)

	modified := config.BackingMap
	assert.Equal(t, "my-user-name", modified["username"], "modified values should be reflected in the map")
	assert.NotContains(t, modified, "sslPEMKeyFile", "final map should not have keys with empty values")
}

func TestFieldsAreAddedToMonitoringConfig(t *testing.T) {
	config := getTestMonitoringConfig()
	config.MonitoringAgentTemplate.SSLPemKeyFile = "my-pem-file"
	config.MonitoringAgentTemplate.Username = "my-user-name"

	err := config.Apply()
	assert.NoError(t, err)

	modified := config.BackingMap
	assert.Equal(t, modified["sslPEMKeyFile"], "my-pem-file")
	assert.Equal(t, modified["username"], "my-user-name")
}

func TestFieldsAreNotRemovedWhenUpdatingMonitoringConfig(t *testing.T) {
	config := getTestMonitoringConfig()
	config.MonitoringAgentTemplate.SSLPemKeyFile = "my-pem-file"
	config.MonitoringAgentTemplate.Username = "my-user-name"

	err := config.Apply()
	assert.NoError(t, err)

	assert.Equal(t, config.BackingMap["logPath"], testMonitoringConfig.BackingMap["logPath"])
	assert.Equal(t, config.BackingMap["logPathWindows"], testMonitoringConfig.BackingMap["logPathWindows"])
}
