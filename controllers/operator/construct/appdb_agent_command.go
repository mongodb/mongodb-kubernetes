package construct

import (
	"fmt"
	"strconv"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
)

// Private constants — verbatim values from MCO's mongodbstatefulset.go and readiness/config/config.go.
const (
	appdbClusterFilePath                = "/var/lib/automation/config/cluster-config.json"
	appdbAgentHealthStatusFilePathValue = "/var/log/mongodb-mms-automation/healthstatus/agent-health-status.json"
	appdbAutomationAgentOptions         = " -skipMongoStart -noDaemonize -useLocalMongoDbTools"

	// Readiness probe logger env var names — from MCO's pkg/readiness/config.
	appdbReadinessProbeLoggerBackups  = "READINESS_PROBE_LOGGER_BACKUPS"
	appdbReadinessProbeLoggerMaxSize  = "READINESS_PROBE_LOGGER_MAX_SIZE"
	appdbReadinessProbeLoggerMaxAge   = "READINESS_PROBE_LOGGER_MAX_AGE"
	appdbReadinessProbeLoggerCompress = "READINESS_PROBE_LOGGER_COMPRESS"
	appdbWithAgentFileLogging         = "MDB_WITH_AGENT_FILE_LOGGING"
	appdbAgentHealthStatusFilePathEnv = "AGENT_STATUS_FILEPATH"
)

// MongodbUserCommand is the bash preamble that sets up the correct UID mapping for mongod.
// Verbatim copy from MCO's mongodbstatefulset.go.
const MongodbUserCommand = `current_uid=$(id -u)
declare -r current_uid
if ! grep -q "${current_uid}" /etc/passwd ; then
sed -e "s/^mongodb:/builder:/" /etc/passwd > /tmp/passwd
echo "mongodb:x:$(id -u):$(id -g):,,,:/:/bin/bash" >> /tmp/passwd
export NSS_WRAPPER_PASSWD=/tmp/passwd
export LD_PRELOAD=libnss_wrapper.so
export NSS_WRAPPER_GROUP=/etc/group
fi
`

// BaseAgentCommand returns the core agent binary invocation flags.
// Verbatim copy from MCO's mongodbstatefulset.go.
func BaseAgentCommand() string {
	return "agent/mongodb-agent -healthCheckFilePath=" + appdbAgentHealthStatusFilePathValue + " -serveStatusPort=5000"
}

// AutomationAgentCommand returns the full command array for the automation agent container.
// withAgentAPIKeyExport detects whether we want to deploy this agent with the agent api key exported;
// it can be used to register the agent with OM.
// Verbatim copy from MCO's mongodbstatefulset.go.
func AutomationAgentCommand(withStatic bool, withAgentAPIKeyExport bool, logLevel v1.LogLevel, logFile string, maxLogFileDurationHours int) []string {
	// This is somewhat undocumented at https://www.mongodb.com/docs/ops-manager/current/reference/mongodb-agent-settings/
	// Not setting the -logFile option make the mongodb-agent log to stdout. Setting -logFile /dev/stdout will result in
	// an error by the agent trying to open /dev/stdout-verbose and still trying to do log rotation.
	// To keep consistent with old behavior not setting the logFile in the config does not log to stdout but keeps
	// the default logFile as defined by DefaultAgentLogFile. Setting the logFile explictly to "/dev/stdout" will log to stdout.
	agentLogOptions := ""
	if logFile == "/dev/stdout" {
		agentLogOptions += " -logLevel " + string(logLevel)
	} else {
		agentLogOptions += " -logFile " + logFile + " -logLevel " + string(logLevel) + " -maxLogFileDurationHrs " + strconv.Itoa(maxLogFileDurationHours)
	}

	if withAgentAPIKeyExport {
		return []string{"/bin/bash", "-c", GetMongodbUserCommandWithAPIKeyExport(withStatic) + BaseAgentCommand() + " -cluster=" + appdbClusterFilePath + appdbAutomationAgentOptions + agentLogOptions}
	}
	return []string{"/bin/bash", "-c", MongodbUserCommand + BaseAgentCommand() + " -cluster=" + appdbClusterFilePath + appdbAutomationAgentOptions + agentLogOptions}
}

// GetMongodbUserCommandWithAPIKeyExport returns the bash preamble that exports AGENT_API_KEY from a file.
// Verbatim copy from MCO's mongodbstatefulset.go.
func GetMongodbUserCommandWithAPIKeyExport(withStatic bool) string {
	agentPrepareScript := ""
	if withStatic {
		agentPrepareScript = "/usr/local/bin/setup-agent-files.sh\n"
	}

	//nolint:gosec //The credentials path is hardcoded in the container.
	return fmt.Sprintf(`%scurrent_uid=$(id -u)
AGENT_API_KEY="$(cat /mongodb-automation/agent-api-key/agentApiKey)"
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
