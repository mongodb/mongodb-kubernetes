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

	for range 10 {
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

func TestEditMmsConfiguration_UpdateConfFile_RamPercentageStripsStockHeap(t *testing.T) {
	confFile := _writeTempFileWithContent(_stockMmsConf(), "conf")
	t.Setenv("CUSTOM_JAVA_MMS_UI_OPTS", "-XX:MaxRAMPercentage=65")
	err := updateConfFile(confFile)
	assert.NoError(t, err)

	content, err := os.ReadFile(confFile)
	assert.NoError(t, err)
	text := string(content)

	assert.Contains(t, text, "JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} ${JAVA_MMS_COMMON_OPTS} -Xss512k -XX:ReservedCodeCacheSize=128m -XX:-OmitStackTraceInFastThrow\"")
	assert.NotContains(t, text, "-Xmx${MMS_HEAP_SIZE}m")
	assert.NotContains(t, text, "-Xms${MMS_HEAP_SIZE}m")
	assert.Contains(t, text, "JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -XX:MaxRAMPercentage=65\"")
	// other lines are untouched
	assert.Contains(t, text, "JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -Dorg.xerial.snappy.tempdir=${APP_DIR}/tmp\"")
	assert.Contains(t, text, "#JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -Xdebug")
}

func TestEditMmsConfiguration_UpdateConfFile_FixedHeapKeepsStockHeap(t *testing.T) {
	confFile := _writeTempFileWithContent(_stockMmsConf(), "conf")
	t.Setenv("CUSTOM_JAVA_MMS_UI_OPTS", "-Xmx4000m -Xms4000m")
	err := updateConfFile(confFile)
	assert.NoError(t, err)

	content, err := os.ReadFile(confFile)
	assert.NoError(t, err)
	assert.Contains(t, string(content), "-Xmx${MMS_HEAP_SIZE}m -Xms${MMS_HEAP_SIZE}m")
}

func TestEditMmsConfiguration_UpdateConfFile_RamPercentageOnlyTouchesOwnProcess(t *testing.T) {
	stock := _stockMmsConf() + "JAVA_DAEMON_OPTS=\"${JAVA_DAEMON_OPTS} -Xmx4352m\"\n"
	confFile := _writeTempFileWithContent(stock, "conf")
	t.Setenv("CUSTOM_JAVA_MMS_UI_OPTS", "-XX:MaxRAMPercentage=65")
	err := updateConfFile(confFile)
	assert.NoError(t, err)

	content, err := os.ReadFile(confFile)
	assert.NoError(t, err)
	assert.Contains(t, string(content), "JAVA_DAEMON_OPTS=\"${JAVA_DAEMON_OPTS} -Xmx4352m\"")
}

func TestEditMmsConfiguration_UpdateConfFile_BackupDaemonRamPercentage(t *testing.T) {
	stock := "JAVA_DAEMON_OPTS=\"${JAVA_DAEMON_OPTS} -Xmx4352m -Xms4352m -DMONGO.BIN.PREFIX=\"\n" + _stockMmsConf()
	confFile := _writeTempFileWithContent(stock, "conf")
	t.Setenv("BACKUP_DAEMON", "something")
	t.Setenv("CUSTOM_JAVA_DAEMON_OPTS", "-XX:MaxRAMPercentage=50")
	err := updateConfFile(confFile)
	assert.NoError(t, err)

	content, err := os.ReadFile(confFile)
	assert.NoError(t, err)
	text := string(content)
	assert.Contains(t, text, "JAVA_DAEMON_OPTS=\"${JAVA_DAEMON_OPTS} -DMONGO.BIN.PREFIX=\"")
	assert.Contains(t, text, "-Xmx${MMS_HEAP_SIZE}m", "the app line must not be touched for the backup daemon")
}

func TestEditMmsConfiguration_UpdateConfFile_RamPercentageIdempotent(t *testing.T) {
	confFile := _writeTempFileWithContent(_stockMmsConf(), "conf")
	t.Setenv("CUSTOM_JAVA_MMS_UI_OPTS", "-XX:MaxRAMPercentage=65")
	for range 3 {
		assert.NoError(t, updateConfFile(confFile))
	}

	content, err := os.ReadFile(confFile)
	assert.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(content), "-XX:MaxRAMPercentage=65"))
}

func TestEditMmsConfiguration_UpdateConfFile_InitialRamPercentageStripsOnlyStockXms(t *testing.T) {
	confFile := _writeTempFileWithContent(_stockMmsConf(), "conf")
	t.Setenv("CUSTOM_JAVA_MMS_UI_OPTS", "-XX:InitialRAMPercentage=50 -Xmx3686m")
	assert.NoError(t, updateConfFile(confFile))

	content, err := os.ReadFile(confFile)
	assert.NoError(t, err)
	text := string(content)
	assert.Contains(t, text, "-Xss512k -Xmx${MMS_HEAP_SIZE}m -XX:ReservedCodeCacheSize=128m")
	assert.NotContains(t, text, "-Xms${MMS_HEAP_SIZE}m")
}

func TestEditMmsConfiguration_UpdateConfFile_MinRamPercentageKeepsStockHeap(t *testing.T) {
	confFile := _writeTempFileWithContent(_stockMmsConf(), "conf")
	t.Setenv("CUSTOM_JAVA_MMS_UI_OPTS", "-XX:MinRAMPercentage=50 -Xmx3686m -Xms3686m")
	assert.NoError(t, updateConfFile(confFile))

	content, err := os.ReadFile(confFile)
	assert.NoError(t, err)
	assert.Contains(t, string(content), "-Xmx${MMS_HEAP_SIZE}m -Xms${MMS_HEAP_SIZE}m")
}

func TestEditMmsConfiguration_UpdateConfFile_RamPercentageKeepsUserXmxAcrossRuns(t *testing.T) {
	confFile := _writeTempFileWithContent(_stockMmsConf(), "conf")
	t.Setenv("CUSTOM_JAVA_MMS_UI_OPTS", "-XX:MaxRAMPercentage=65 -Xmx2g")
	for range 3 {
		assert.NoError(t, updateConfFile(confFile))
	}

	content, err := os.ReadFile(confFile)
	assert.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(content), "JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -XX:MaxRAMPercentage=65 -Xmx2g\""))
}

func TestRemoveStockHeapFlags(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "stock line",
			line: "JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -Xss512k -Xmx${MMS_HEAP_SIZE}m -Xms${MMS_HEAP_SIZE}m -XX:-OmitStackTraceInFastThrow\"",
			want: "JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -Xss512k -XX:-OmitStackTraceInFastThrow\"",
		},
		{
			name: "flags right after the quote",
			line: "JAVA_MMS_UI_OPTS=\"-Xmx8g -Xms8g -Xss512k\"",
			want: "JAVA_MMS_UI_OPTS=\"-Xss512k\"",
		},
		{
			name: "indented line with a space after the equals sign",
			line: "  JAVA_MMS_UI_OPTS= \"${JAVA_MMS_UI_OPTS} -Xmx4352m -Xms4352m\"",
			want: "  JAVA_MMS_UI_OPTS= \"${JAVA_MMS_UI_OPTS}\"",
		},
		{
			name: "commented out line is untouched",
			line: "#JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -Xmx4352m\"",
			want: "#JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -Xmx4352m\"",
		},
		{
			name: "other process line is untouched",
			line: "JAVA_DAEMON_OPTS=\"${JAVA_DAEMON_OPTS} -Xmx4352m\"",
			want: "JAVA_DAEMON_OPTS=\"${JAVA_DAEMON_OPTS} -Xmx4352m\"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, removeStockHeapFlags(tt.line, "JAVA_MMS_UI_OPTS", true, true))
		})
	}
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

// _stockMmsConf is the relevant part of the mms.conf shipped in the OM 8.0.5 image.
func _stockMmsConf() string {
	return "MMS_HEAP_SIZE=${MMS_HEAP_SIZE:-8096}\n" +
		"JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} ${JAVA_MMS_COMMON_OPTS} -Xss512k -Xmx${MMS_HEAP_SIZE}m -Xms${MMS_HEAP_SIZE}m -XX:ReservedCodeCacheSize=128m -XX:-OmitStackTraceInFastThrow\"\n" +
		"JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -Dorg.xerial.snappy.tempdir=${APP_DIR}/tmp\"\n" +
		"#JAVA_MMS_UI_OPTS=\"${JAVA_MMS_UI_OPTS} -Xdebug -Xrunjdwp:transport=dt_socket,server=y,suspend=n,address=5005\"\n"
}
