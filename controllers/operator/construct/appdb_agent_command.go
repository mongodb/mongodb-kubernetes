package construct

import (
	"fmt"
	"regexp"
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
	settings, err := appDBAgentSettings(sts, appDB, podVars)
	if err != nil {
		return corev1.ConfigMap{}, err
	}
	return agentSettingsConfigMap(sts, settings)
}

// appDBAgentSettings builds the complete AppDB agent settings map. It owns the map:
// reserved keys are filtered before insertion, so callers do not delete them afterwards.
func appDBAgentSettings(sts appsv1.StatefulSet, appDB *omv1.AppDBSpec, podVars *env.PodEnvVars) (mdbv1.StartupParameters, error) {
	parameters := appDB.AutomationAgent.StartupParameters
	agent := AgentContainer(sts)
	// Reject a custom AGENT_FLAGS here too, so AppDB behaves like every other resource type.
	if agent != nil {
		if err := ValidateNoCustomAgentFlags(*agent); err != nil {
			return nil, err
		}
		if err := ValidateNoRuntimeAgentSettingsEnv(*agent); err != nil {
			return nil, err
		}
	}
	var variables map[string]string
	if agent != nil {
		variables = env.ToMap(agent.Env...)
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
	overrides := effectiveAgentSettingsOverrides(parameters)
	_, rawKeyOverride := overrides["mmsApiKey"]
	for key, value := range overrides {
		if key == "mmsApiKey" || key == "overrideLocalHost" {
			continue
		}
		// A customer mmsApiKeyExec is a shell command for the agent. Expanding variables here
		// would substitute the operator view and corrupt the command.
		if key != "mmsApiKeyExec" {
			if value == "" {
				// The legacy AppDB command rendered an empty value as a bare flag, which meant true
				// for boolean options. The settings file has no type information, so keep that meaning.
				value = "true"
			} else if agent != nil {
				value = expandLiteralShellVariables(value, *agent, variables)
			}
		}
		settings[key] = value
	}
	// The legacy command appended the monitoring project after customer startup options, so the
	// operator project wins a duplicate mmsGroupId. A customer mmsApiKeyExec still wins, because
	// the agent runs the exec command in preference to mmsApiKey.
	if ShouldEnableMonitoring(podVars) {
		settings["mmsGroupId"] = podVars.ProjectID
	}
	if (ShouldEnableMonitoring(podVars) || rawKeyOverride) && settings["mmsApiKeyExec"] == "" {
		settings["mmsApiKeyExec"] = "cat '/mongodb-automation/agent-api-key/agentApiKey'"
	}
	return settings, nil
}

func AppDBOverrideLocalHostArg(parameters mdbv1.StartupParameters, externalDomain *string, multiCluster bool, namespace, clusterDomain string) (string, bool) {
	if externalDomain != nil {
		return "-overrideLocalHost=$(POD_NAME)." + *externalDomain, true
	}
	if multiCluster {
		return "-overrideLocalHost=$(POD_NAME)-svc." + namespace + ".svc." + clusterDomain, true
	}
	hostname, overridden := AgentSettingsOverrides(parameters)["overrideLocalHost"]
	if overridden {
		return "-overrideLocalHost=" + hostname, false
	}
	return "", false
}

var shellVariableRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

func expandLiteralShellVariables(value string, agent corev1.Container, variables map[string]string) string {
	return shellVariableRef.ReplaceAllStringFunc(value, func(ref string) string {
		match := shellVariableRef.FindStringSubmatch(ref)
		name := match[1]
		if name == "" {
			name = match[2]
		}
		if AgentEnvironmentIsRuntime(agent, name) {
			return ref
		}
		if literal, ok := variables[name]; ok {
			return literal
		}
		return ""
	})
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
