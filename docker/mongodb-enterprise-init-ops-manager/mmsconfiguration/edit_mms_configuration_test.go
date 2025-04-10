package main

import (
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEditMmsConfiguration_UpdateConfFile_Mms(t *testing.T) {
	confFile := _createTestConfFile()
	t.Setenv("CUSTOM_JAVA_MMS_UI_OPTS", "-Xmx4000m -Xms4000m")
	err := updateConfFile(confFile)
	assert.NoError(t, err)
	updatedContent := _readLinesFromFile(confFile)
	assert.Equal(t, updatedContent[7], "JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -Xmx4000m -Xms4000m\"")
}

func TestEditMmsConfiguration_UpdateConfFile_BackupDaemon(t *testing.T) {
	confFile := _createTestConfFile()

	t.Setenv("BACKUP_DAEMON", "something")
	t.Setenv("CUSTOM_JAVA_DAEMON_OPTS", "-Xmx4000m -Xms4000m")
	err := updateConfFile(confFile)
	assert.NoError(t, err)
}

func TestEditMmsConfiguration_GetOmPropertiesFromEnvVars(t *testing.T) {
	val := fmt.Sprintf("test%d", rand.Intn(1000))
	key := "OM_PROP_test_edit_mms_configuration_get_om_props"
	t.Setenv(key, val)
	props := getOmPropertiesFromEnvVars()
	assert.Equal(t, props["test.edit.mms.configuration.get.om.props"], val)
}

func TestEditMmsConfiguration_UpdatePropertiesFile(t *testing.T) {
	newProperties := map[string]string{
		"mms.test.prop":     "somethingNew",
		"mms.test.prop.new": "400",
	}
	propFile := _createTestPropertiesFile()
	err := updatePropertiesFile(propFile, newProperties)
	assert.NoError(t, err)

	updatedContent := _readLinesFromFile(propFile)
	assert.Equal(t, updatedContent[0], "mms.prop=1234")
	assert.Equal(t, updatedContent[1], "mms.test.prop5=")
	assert.Equal(t, updatedContent[2], "mms.test.prop=somethingNew")
	assert.Equal(t, updatedContent[3], "mms.test.prop.new=400")
}

func _createTestConfFile() string {
	contents := "JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -Xmx4352m -Xss328k  -Xms4352m -XX:NewSize=600m -Xmn1500m -XX:ReservedCodeCacheSize=128m -XX:-OmitStackTraceInFastThrow\"\n"
	contents += "JAVA_DAEMON_OPTS= \"${JAVA_DAEMON_OPTS} -DMONGO.BIN.PREFIX=\"\n\n"
	return _writeTempFileWithContent(contents, "conf")
}

func _createTestPropertiesFile() string {
	contents := "mms.prop=1234\nmms.test.prop5=\nmms.test.prop=something"
	return _writeTempFileWithContent(contents, "prop")
}

func _readLinesFromFile(name string) []string {
	content, _ := os.ReadFile(name)
	return strings.Split(string(content), "\n")
}

func _writeTempFileWithContent(content string, prefix string) string {
	tmpfile, _ := os.CreateTemp("", prefix)

	_, _ = tmpfile.WriteString(content)

	_ = tmpfile.Close()

	return tmpfile.Name()
}
