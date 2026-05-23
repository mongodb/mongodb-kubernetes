package construct_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mongodb/mongodb-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct"
)

func TestHeadlessAgentCommand_ContainsClusterFlag(t *testing.T) {
	cmd := construct.HeadlessAutomationAgentCommand(v1.LogLevel("INFO"), "/dev/stdout", 24)
	last := cmd[len(cmd)-1]
	assert.Contains(t, last, "-cluster="+construct.HeadlessClusterFilePath)
	assert.NotContains(t, last, "-mmsBaseUrl")
	assert.Contains(t, last, "setup-agent-files.sh")
	assert.Contains(t, last, "-binariesFixedPath=")
	assert.Contains(t, last, "mongodb_marker")
	assert.Contains(t, last, "-operatorMode=true")
	assert.NotContains(t, last, "-useLocalMongoDbTools")
}

func TestHeadlessAgentEnvVars_ContainsHeadlessFlag(t *testing.T) {
	envs := construct.HeadlessAgentEnvVars("my-config-secret")
	names := make([]string, len(envs))
	for i, e := range envs {
		names[i] = e.Name
	}
	assert.Contains(t, names, "HEADLESS_AGENT")
	assert.Contains(t, names, "AUTOMATION_CONFIG_MAP")
	assert.Contains(t, names, "AGENT_STATUS_FILEPATH")
	assert.NotContains(t, names, "BASE_URL")
	assert.NotContains(t, names, "GROUP_ID")
}

func TestHeadlessAgentEnvVars_HeadlessAgentIsTrue(t *testing.T) {
	envs := construct.HeadlessAgentEnvVars("my-config-secret")
	for _, e := range envs {
		if e.Name == "HEADLESS_AGENT" {
			assert.Equal(t, "true", e.Value)
			return
		}
	}
	t.Fatal("HEADLESS_AGENT env var not found")
}

func TestHeadlessAgentEnvVars_AutomationConfigMapSecretName(t *testing.T) {
	envs := construct.HeadlessAgentEnvVars("my-config-secret")
	for _, e := range envs {
		if e.Name == "AUTOMATION_CONFIG_MAP" {
			assert.Equal(t, "my-config-secret", e.Value)
			return
		}
	}
	t.Fatal("AUTOMATION_CONFIG_MAP env var not found")
}

func TestAgentDownloadsVolume_IsEmptyDir(t *testing.T) {
	vol := construct.AgentDownloadsVolume()
	assert.Equal(t, "agent-downloads", vol.Name)
	assert.NotNil(t, vol.EmptyDir)
}

func TestHeadlessAgentCommand_FileLog_ContainsLogFileAndDuration(t *testing.T) {
	cmd := construct.HeadlessAutomationAgentCommand(v1.LogLevel("INFO"), "/var/log/agent.log", 24)
	last := cmd[len(cmd)-1]
	assert.Contains(t, last, "-logFile /var/log/agent.log")
	assert.Contains(t, last, "-maxLogFileDurationHrs 24")
	assert.NotContains(t, last, "/dev/stdout")
}

func TestHeadlessAgentCommand_EmptyLogLevel_NoLogLevelFlag(t *testing.T) {
	cmd := construct.HeadlessAutomationAgentCommand(v1.LogLevel(""), "/dev/stdout", 24)
	assert.NotContains(t, cmd[len(cmd)-1], "-logLevel")
}
