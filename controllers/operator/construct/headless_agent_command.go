package construct

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	// HeadlessClusterFilePath is the path inside the static agent container where
	// the automation config Secret is mounted.
	HeadlessClusterFilePath = appdbClusterFilePath

	// HeadlessConfigVolumeName is the name of the volume carrying the automation config Secret.
	HeadlessConfigVolumeName = "headless-config"

	// headlessNonStaticMountPath is the directory where agent-launcher.sh reads cluster-config.json
	// in non-static (mongodb-enterprise-database) containers.
	headlessNonStaticMountPath = "/var/lib/mongodb-automation"

	// headlessStaticMountPath is the directory where the AppDB-style agent reads cluster-config.json
	// in static containers.
	headlessStaticMountPath = "/var/lib/automation/config"

	// HeadlessAgentEnvName is the env var name that marks a container as running in headless mode.
	HeadlessAgentEnvName = "HEADLESS_AGENT"

	headlessAutomationConfigMapEnv   = "AUTOMATION_CONFIG_MAP"
	headlessAgentDownloadsVolumeName = "agent-downloads"

	// headlessMongodBinDir is the directory where the static headless agent expects mongod binaries.
	// Must match agent-launcher.sh's mdb_downloads_dir + "/mongod/bin".
	headlessMongodBinDir = "/var/lib/mongodb-mms-automation/mongod/bin"

	// headlessStaticAgentOptions are the agent flags for enterprise static headless mode.
	// Uses -operatorMode=true (what agent-launcher.sh passes for static containers) instead of
	// -useLocalMongoDbTools (which is the non-static option in appdbAutomationAgentOptions).
	headlessStaticAgentOptions = " -skipMongoStart -noDaemonize -operatorMode=true"
)

// headlessStaticMongodSetup is the bash snippet that waits for the mongodb-enterprise-database
// container (identified by the mongodb_marker process) and creates symlinks to its mongod/mongos
// binaries via the shared process namespace. This mirrors what agent-launcher.sh does for regular
// static mode, but inline since HeadlessAutomationAgentCommand bypasses agent-launcher.sh.
//
// It also creates the healthstatus directory that the agent requires to write its health file.
// In AppDB/Community an EmptyDir volume is mounted there; in headless static mode there is no
// such volume, so we create the directory inside the existing log mount instead.
const headlessStaticMongodSetup = `mkdir -p /var/log/mongodb-mms-automation/healthstatus
ln -sf /journal /data/
WAIT_TIME=5
MAX_WAIT=300
ELAPSED_TIME=0
while [ ${ELAPSED_TIME} -lt ${MAX_WAIT} ]; do
  MONGOD_PID=$(ps aux | grep "mongodb_marker" | grep -v grep | awk '{print $2}') || true
  if [ -n "${MONGOD_PID}" ] && [ "${MONGOD_PID}" -ne 0 ]; then
    break
  fi
  sleep ${WAIT_TIME}
  ELAPSED_TIME=$((ELAPSED_TIME + WAIT_TIME))
done
if [ -z "${MONGOD_PID}" ] || [ "${MONGOD_PID}" -eq 0 ]; then
  echo "mongodb_marker PID not found within ${MAX_WAIT}s"
  exit 1
fi
mkdir -p ` + headlessMongodBinDir + `
ln -sf "/proc/${MONGOD_PID}/root/bin/mongod" ` + headlessMongodBinDir + `/mongod
ln -sf "/proc/${MONGOD_PID}/root/bin/mongos" ` + headlessMongodBinDir + `/mongos
`

// HeadlessClusterConfigMountPath returns the directory where the automation config Secret
// should be mounted, based on whether the container uses the static or non-static architecture.
func HeadlessClusterConfigMountPath(isStatic bool) string {
	if isStatic {
		return headlessStaticMountPath
	}
	return headlessNonStaticMountPath
}

// HeadlessAgentBinaryInitContainer returns an init container that copies the automation agent
// binary from the agent image into the shared agent emptyDir. Required in non-static headless
// mode because the agent binary is normally downloaded from Ops Manager at runtime.
//
// The agent emptyDir (util.PvMms) is mounted at /mms (no subpath) so the init container can
// write to the mongodb-automation/files/ subdirectory that the main container reads via its
// subpath mount at /mongodb-automation.
func HeadlessAgentBinaryInitContainer(agentImage string) corev1.Container {
	return corev1.Container{
		Name:  "headless-agent-binary-init",
		Image: agentImage,
		Command: []string{
			"/bin/sh", "-c",
			"mkdir -p /mms/" + util.PvcMmsHome + "/files && " +
				"cp /agent/mongodb-agent /mms/" + util.PvcMmsHome + "/files/mongodb-mms-automation-agent && " +
				"chmod +x /mms/" + util.PvcMmsHome + "/files/mongodb-mms-automation-agent",
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: util.PvMms, MountPath: "/mms"},
		},
	}
}

// HeadlessMongodBinaryInitContainer returns an init container that copies the mongod binary
// from the MongoDB image into the shared agent emptyDir. Required in non-static headless mode
// because there is no Ops Manager to download MongoDB from at runtime.
//
// The binary is placed at the path agent-launcher.sh uses for -binariesFixedPath.
func HeadlessMongodBinaryInitContainer(mongodbImage string) corev1.Container {
	mongodBinDir := "/mms/mongodb-mms-automation/mongod/bin"
	return corev1.Container{
		Name:  "headless-mongod-binary-init",
		Image: mongodbImage,
		Command: []string{
			"/bin/sh", "-c",
			"mkdir -p " + mongodBinDir + " && " +
				"cp /usr/bin/mongod " + mongodBinDir + "/mongod && " +
				"cp /usr/bin/mongos " + mongodBinDir + "/mongos && " +
				"chmod +x " + mongodBinDir + "/mongod " + mongodBinDir + "/mongos",
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: util.PvMms, MountPath: "/mms"},
		},
	}
}

// HeadlessAutomationAgentCommand returns the full command for the automation agent
// container in headless mode (static architecture only). Agents read from a local
// cluster-config.json Secret mount instead of connecting to Ops Manager.
//
// setup-agent-files.sh must run first: it populates the database-scripts EmptyDir
// with probe.sh and readinessprobe that the liveness/readiness/startup probes exec.
// Without it the probes fail immediately because the volume is empty.
func HeadlessAutomationAgentCommand(logLevel v1.LogLevel, logFile string, maxLogFileDurationHours int) []string {
	logLevelOpt := ""
	if logLevel != "" {
		logLevelOpt = " -logLevel " + string(logLevel)
	}

	logOpts := ""
	if logFile == "/dev/stdout" {
		logOpts = logLevelOpt
	} else {
		logOpts = " -logFile " + logFile + logLevelOpt + " -maxLogFileDurationHrs " + strconv.Itoa(maxLogFileDurationHours)
	}
	cmd := "/usr/local/bin/setup-agent-files.sh\n" + headlessStaticMongodSetup + MongodbUserCommand + BaseAgentCommand() +
		" -cluster=" + HeadlessClusterFilePath + headlessStaticAgentOptions +
		" -binariesFixedPath=" + headlessMongodBinDir + logOpts
	return []string{"/bin/bash", "-c", cmd}
}

// HeadlessAgentEnvVars returns the env vars that put an agent container into headless mode.
// configSecretName is the name of the Secret holding cluster-config.json.
func HeadlessAgentEnvVars(configSecretName string) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: HeadlessAgentEnvName, Value: "true"},
		{Name: headlessAutomationConfigMapEnv, Value: configSecretName},
		// AGENT_STATUS_FILEPATH tells the readinessprobe where the agent writes its health file.
		// Must match the -healthCheckFilePath flag passed to the agent binary.
		{Name: "AGENT_STATUS_FILEPATH", Value: appdbAgentHealthStatusFilePathValue},
		{
			Name: "POD_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "metadata.namespace",
				},
			},
		},
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
