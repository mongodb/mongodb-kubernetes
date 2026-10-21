package construct

import (
	"fmt"
	"maps"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

const (
	appdbClusterFilePath                = "/var/lib/automation/config/cluster-config.json"
	appdbAgentHealthStatusFilePathValue = "/var/log/mongodb-mms-automation/healthstatus/agent-health-status.json"

	appdbReadinessProbeLoggerBackups  = "READINESS_PROBE_LOGGER_BACKUPS"
	appdbReadinessProbeLoggerMaxSize  = "READINESS_PROBE_LOGGER_MAX_SIZE"
	appdbReadinessProbeLoggerMaxAge   = "READINESS_PROBE_LOGGER_MAX_AGE"
	appdbReadinessProbeLoggerCompress = "READINESS_PROBE_LOGGER_COMPRESS"
	appdbWithAgentFileLogging         = "MDB_WITH_AGENT_FILE_LOGGING"
	appdbAgentHealthStatusFilePathEnv = "AGENT_STATUS_FILEPATH"
)

// AppDBAgentSettingsConfigMap preserves AppDB defaults independently of database defaults.
func AppDBAgentSettingsConfigMap(sts appsv1.StatefulSet, appDB *omv1.AppDBSpec, podVars *env.PodEnvVars) (corev1.ConfigMap, error) {
	parameters := appDB.AutomationAgent.StartupParameters
	// Reject a custom AGENT_FLAGS here too, so AppDB behaves like every other resource type.
	if agent := AgentContainer(sts); agent != nil {
		if err := ValidateNoCustomAgentFlags(*agent, parameters); err != nil {
			return corev1.ConfigMap{}, err
		}
	}
	settings := mdbv1.StartupParameters{
		"cluster":              appdbClusterFilePath,
		"healthCheckFilePath":  appdbAgentHealthStatusFilePathValue,
		"serveStatusPort":      "5000",
		"skipMongoStart":       strconv.FormatBool(true),
		"noDaemonize":          strconv.FormatBool(true),
		"useLocalMongoDbTools": strconv.FormatBool(true),
		"logLevel":             string(appDB.GetAgentLogLevel()),
	}
	if logFile := appDB.GetAgentLogFile(); logFile != "/dev/stdout" {
		settings["logFile"] = logFile
		settings["maxLogFileDurationHrs"] = strconv.Itoa(appDB.GetAgentMaxLogFileDurationHours())
	}
	if ShouldEnableMonitoring(podVars) {
		settings["mmsGroupId"] = podVars.ProjectID
	}
	maps.Copy(settings, AgentSettingsOverrides(parameters))
	_, rawKeyOverride := settings["mmsApiKey"]
	if (ShouldEnableMonitoring(podVars) || rawKeyOverride) && settings["mmsApiKeyExec"] == "" {
		settings["mmsApiKeyExec"] = "cat '/mongodb-automation/agent-api-key/agentApiKey'"
	}
	delete(settings, "mmsApiKey")
	delete(settings, "overrideLocalHost")
	return agentSettingsConfigMap(sts, settings)
}

// downloadCustomAgentIfSet returns a bash snippet that downloads and installs
// a custom agent from MDB_CUSTOM_AGENT_URL when set, setting the local
// agent_binary variable for the final invocation. Only used in
// non-static mode — in static mode the agent image is already built with
// the correct binary.
func downloadCustomAgentIfSet() string {
	return `if [[ -n "${MDB_CUSTOM_AGENT_URL:-}" ]]; then
  echo "Using custom agent URL: ${MDB_CUSTOM_AGENT_URL}"
  pushd /tmp >/dev/null || true
  if ! curl --location --silent --retry 3 --fail -v --output automation-agent.tar.gz "${MDB_CUSTOM_AGENT_URL}" 2>&1; then
    echo "Error: failed to download custom agent from ${MDB_CUSTOM_AGENT_URL}"
    exit 1
  fi
  if ! tar -xzf automation-agent.tar.gz; then
    echo "Error: failed to extract custom agent tarball"
    exit 1
  fi
  cp mongodb-mms-automation-agent-*/mongodb-mms-automation-agent /tmp/mongodb-agent
  rm -rf /tmp/automation-agent.tar.gz /tmp/mongodb-mms-automation-agent-*
  chmod +x /tmp/mongodb-agent
  agent_binary=/tmp/mongodb-agent
  popd >/dev/null || true
fi
`
}

// AutomationAgentCommand preserves bootstrap and forwards native settings arguments verbatim.
func AutomationAgentCommand(withStatic bool) []string {
	downloadSnippet := ""
	if !withStatic {
		downloadSnippet = downloadCustomAgentIfSet()
	}

	return []string{"/bin/bash", "-c", GetMongodbUserCommand(withStatic) + downloadSnippet + `${agent_binary:-agent/mongodb-agent} "$@"`, "mongodb-agent"}
}

// GetMongodbUserCommand returns the bash preamble for the automation agent.
func GetMongodbUserCommand(withStatic bool) string {
	agentPrepareScript := ""
	if withStatic {
		agentPrepareScript = "/usr/local/bin/setup-agent-files.sh\n"
	}

	return fmt.Sprintf(`%scurrent_uid=$(id -u)
declare -r current_uid
if ! grep -q "${current_uid}" /etc/passwd ; then
sed -e "s/^mongodb:/builder:/" /etc/passwd > /tmp/passwd
echo "mongodb:x:$(id -u):$(id -g):,,,:/:/bin/bash" >> /tmp/passwd
export NSS_WRAPPER_PASSWD=/tmp/passwd
export LD_PRELOAD=libnss_wrapper.so
export NSS_WRAPPER_GROUP=/etc/group
fi
`, agentPrepareScript)
}
