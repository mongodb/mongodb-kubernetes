package construct

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
)

const (
	// HeadlessClusterFilePath is the path inside the agent container where
	// the automation config Secret is mounted.
	HeadlessClusterFilePath = appdbClusterFilePath

	headlessAgentEnvName             = "HEADLESS_AGENT"
	headlessAutomationConfigMapEnv   = "AUTOMATION_CONFIG_MAP"
	headlessAgentDownloadsVolumeName = "agent-downloads"
)

// HeadlessAutomationAgentCommand returns the full command for the automation agent
// container in headless mode. Agents read from a local cluster-config.json Secret
// mount instead of connecting to Ops Manager.
func HeadlessAutomationAgentCommand(logLevel v1.LogLevel, logFile string, maxLogFileDurationHours int) []string {
	logOpts := ""
	if logFile == "/dev/stdout" {
		logOpts = " -logLevel " + string(logLevel)
	} else {
		logOpts = " -logFile " + logFile +
			" -logLevel " + string(logLevel) +
			" -maxLogFileDurationHrs " + strconv.Itoa(maxLogFileDurationHours)
	}
	cmd := MongodbUserCommand + BaseAgentCommand() +
		" -cluster=" + HeadlessClusterFilePath + appdbAutomationAgentOptions + logOpts
	return []string{"/bin/bash", "-c", cmd}
}

// HeadlessAgentEnvVars returns the env vars that put an agent container into headless mode.
// configSecretName is the name of the Secret holding cluster-config.json.
func HeadlessAgentEnvVars(configSecretName string) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: headlessAgentEnvName, Value: "true"},
		{Name: headlessAutomationConfigMapEnv, Value: configSecretName},
	}
}

// AgentDownloadsVolume returns an emptyDir volume required by the agent for caching
// downloaded binaries. Present in both headless and online modes so that migrating
// headless → online does not require a pod restart solely for volume addition.
func AgentDownloadsVolume() corev1.Volume {
	return corev1.Volume{
		Name:         headlessAgentDownloadsVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	}
}
