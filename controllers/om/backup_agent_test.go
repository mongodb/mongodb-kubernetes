package om

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

var testBackupAgentConfig = *getTestBackupConfig()

func getLinuxUrls(config BackupAgentConfig) map[string]interface{} {
	return config.BackingMap["urls"].(map[string]interface{})["linux"].(map[string]interface{})
}

func getTestBackupConfig() *BackupAgentConfig {
	bac, _ := BuildBackupAgentConfigFromBytes(loadBytesFromTestData("backup_config.json"))
	return bac
}

func TestFieldsAreUpdatedBackupConfig(t *testing.T) {
	config := getTestBackupConfig()
	config.BackupAgentTemplate.Username = "my-backup-user-name"
	config.BackupAgentTemplate.SSLPemKeyFile = "my-backup-pem-file"

	_ = config.Apply()

	assert.Equal(t, config.BackingMap["username"], "my-backup-user-name")
	assert.Equal(t, config.BackingMap["sslPEMKeyFile"], "my-backup-pem-file")
}

func TestBackupFieldsAreNotLost(t *testing.T) {
	config := getTestBackupConfig()
	config.EnableX509Authentication("namespace")

	assert.Contains(t, config.BackingMap, "logPath")
	assert.Contains(t, config.BackingMap, "logRotate")
	assert.Contains(t, config.BackingMap, "urls")

	_ = config.Apply()

	assert.Equal(t, config.BackingMap["logPath"], testBackupAgentConfig.BackingMap["logPath"])
	assert.Equal(t, config.BackingMap["logRotate"], testBackupAgentConfig.BackingMap["logRotate"])
	assert.Equal(t, config.BackingMap["urls"], testBackupAgentConfig.BackingMap["urls"])
}

func TestNestedFieldsAreNotLost(t *testing.T) {
	config := getTestBackupConfig()

	config.EnableX509Authentication("namespace")

	_ = config.Apply()

	urls := config.BackingMap["urls"].(map[string]interface{})

	assert.Contains(t, urls, "linux")
	assert.Contains(t, urls, "osx")
	assert.Contains(t, urls, "windows")

	linuxUrls := urls["linux"].(map[string]interface{})

	testUrls := getLinuxUrls(testBackupAgentConfig)

	assert.Equal(t, linuxUrls["default"], testUrls["default"])
	assert.Equal(t, linuxUrls["ppc64le_rhel7"], testUrls["ppc64le_rhel7"])
	assert.Equal(t, linuxUrls["ppc64le_ubuntu1604"], testUrls["ppc64le_ubuntu1604"])
}

func TestFieldsCanBeDeleted(t *testing.T) {
	config := getTestBackupConfig()

	config.BackupAgentTemplate.SSLPemKeyFile = util.MergoDelete

	_ = config.Apply()

	assert.Equal(t, config.BackingMap["username"], testBackupAgentConfig.BackingMap["username"])
	assert.NotContains(t, config.BackingMap, "sslPEMKeyFile")
}
