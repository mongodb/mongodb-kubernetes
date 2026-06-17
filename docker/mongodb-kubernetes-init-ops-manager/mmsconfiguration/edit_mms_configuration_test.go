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

	content, err := os.ReadFile(confFile)
	assert.NoError(t, err)
	assert.Contains(t, string(content), "JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -Xmx4000m -Xms4000m\"")
	assert.Contains(t, string(content), "## This is the custom JVM configuration set by the Operator")
}

func TestEditMmsConfiguration_UpdateConfFile_BackupDaemon(t *testing.T) {
	confFile := _createTestConfFile()

	t.Setenv("BACKUP_DAEMON", "something")
	t.Setenv("CUSTOM_JAVA_DAEMON_OPTS", "-Xmx4000m -Xms4000m")
	err := updateConfFile(confFile)
	assert.NoError(t, err)
}

// TestEditMmsConfiguration_UpdateConfFile_Idempotent verifies that running
// updateConfFile many times does not accumulate duplicate JVM param blocks.
// The file must contain the param line exactly once regardless of how many
// reconciliation cycles have occurred.
func TestEditMmsConfiguration_UpdateConfFile_Idempotent(t *testing.T) {
	confFile := _createTestConfFile()
	t.Setenv("CUSTOM_JAVA_MMS_UI_OPTS", "-Xmx4000m -Xms4000m")

	for i := 0; i < 10; i++ {
		err := updateConfFile(confFile)
		assert.NoError(t, err)
	}

	content, err := os.ReadFile(confFile)
	assert.NoError(t, err)

	occurrences := strings.Count(string(content), "JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -Xmx4000m -Xms4000m\"")
	assert.Equal(t, 1, occurrences, "JVM param line must appear exactly once after repeated reconciliations")
}

// TestEditMmsConfiguration_UpdateConfFile_ReplacesExistingBlock verifies that
// a pre-existing operator block written by an older pod cycle is replaced
// rather than duplicated when the params change.
func TestEditMmsConfiguration_UpdateConfFile_ReplacesExistingBlock(t *testing.T) {
	confFile := _createTestConfFile()

	t.Setenv("CUSTOM_JAVA_MMS_UI_OPTS", "-Xmx2000m -Xms2000m")
	err := updateConfFile(confFile)
	assert.NoError(t, err)

	t.Setenv("CUSTOM_JAVA_MMS_UI_OPTS", "-Xmx8000m -Xms8000m")
	err = updateConfFile(confFile)
	assert.NoError(t, err)

	content, err := os.ReadFile(confFile)
	assert.NoError(t, err)

	assert.NotContains(t, string(content), "-Xmx2000m", "stale JVM params from a previous cycle must not remain in the file")
	assert.Contains(t, string(content), "-Xmx8000m", "updated JVM params must be present in the file")
}

// TestEditMmsConfiguration_UpdateConfFile_PreservesBaseContent simulates a
// file that has accumulated many duplicate operator blocks (the original bug)
// and verifies that after one reconciliation: all duplicates are removed, the
// base OM content that predates any operator writes is fully preserved, and
// exactly one fresh operator block is present.
func TestEditMmsConfiguration_UpdateConfFile_PreservesBaseContent(t *testing.T) {
	baseContent := "JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -Xmx4352m -Xss328k  -Xms4352m -XX:NewSize=600m -Xmn1500m -XX:ReservedCodeCacheSize=128m -XX:-OmitStackTraceInFastThrow\"\n" +
		"JAVA_DAEMON_OPTS= \"${JAVA_DAEMON_OPTS} -DMONGO.BIN.PREFIX=\"\n\n"

	operatorBlock := func(params string) string {
		return getJvmParamDocString() + "JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} " + params + "\"\n"
	}
	accumulated := baseContent + operatorBlock("-Xmx1000m") + operatorBlock("-Xmx2000m") + operatorBlock("-Xmx3000m")
	confFile := _writeTempFileWithContent(accumulated, "conf")

	t.Setenv("CUSTOM_JAVA_MMS_UI_OPTS", "-Xmx4000m -Xms4000m")
	err := updateConfFile(confFile)
	assert.NoError(t, err)

	content, err := os.ReadFile(confFile)
	assert.NoError(t, err)
	text := string(content)

	// Base content must be intact.
	assert.Contains(t, text, "JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -Xmx4352m -Xss328k")
	assert.Contains(t, text, "JAVA_DAEMON_OPTS= \"${JAVA_DAEMON_OPTS} -DMONGO.BIN.PREFIX=\"")

	// All stale operator params must be gone.
	assert.NotContains(t, text, "-Xmx1000m")
	assert.NotContains(t, text, "-Xmx2000m")
	assert.NotContains(t, text, "-Xmx3000m")

	// Exactly one fresh operator block must be present.
	assert.Equal(t, 1, strings.Count(text, "## This is the custom JVM configuration set by the Operator"))
	assert.Equal(t, 1, strings.Count(text, "JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -Xmx4000m -Xms4000m\""))
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
