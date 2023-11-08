#!/usr/bin/env bash
set -Eeou pipefail

export MDB_LOG_FILE_AGENT_LAUNCHER_SCRIPT="${MMS_LOG_DIR}/agent-launcher-script.log"

# We start tailing script logs immediately to not miss anything.
# -F flag is equivalent to --follow=name --retry.
# -n0 parameter is instructing tail to show only new lines (by default tail is showing last 10 lines)
tail -F -n0 "${MDB_LOG_FILE_AGENT_LAUNCHER_SCRIPT}" 2> /dev/null &

# shellcheck disable=SC1091
source /opt/scripts/agent-launcher-lib.sh

# all the following MDB_LOG_FILE_* env var should be defined in container's env vars
tail -F -n0 "${MDB_LOG_FILE_AUTOMATION_AGENT_VERBOSE}" 2> /dev/null | json_log 'automation-agent-verbose' &
tail -F -n0 "${MDB_LOG_FILE_AUTOMATION_AGENT_STDERR}" 2> /dev/null | json_log 'automation-agent-stderr' &
tail -F -n0 "${MDB_LOG_FILE_AUTOMATION_AGENT}" 2> /dev/null | json_log 'automation-agent' &
tail -F -n0 "${MDB_LOG_FILE_MONITORING_AGENT}" 2> /dev/null | json_log 'monitoring-agent' &
tail -F -n0 "${MDB_LOG_FILE_BACKUP_AGENT}" 2> /dev/null | json_log 'backup-agent' &
tail -F -n0 "${MDB_LOG_FILE_MONGODB}" 2> /dev/null | json_log 'mongodb' &
tail -F -n0 "${MDB_LOG_FILE_MONGODB_AUDIT}" 2> /dev/null | json_log 'mongodb-audit' &

# This is the directory corresponding to 'options.downloadBase' in the automation config - the directory where
# the agent will extract MongoDB binaries to
mdb_downloads_dir="/var/lib/mongodb-mms-automation"

# The path to the automation config file in case the agent is run in headless mode
cluster_config_file="/var/lib/mongodb-automation/cluster-config.json"

# Always copy the tools provided by the init container to the directory where the agent looks for all binaries
cp -r /opt/scripts/tools/* "${mdb_downloads_dir}"

# file required by Automation Agents of authentication is enabled.
touch "${mdb_downloads_dir}/keyfile"
chmod 600 "${mdb_downloads_dir}/keyfile"

ensure_certs_symlinks

# Ensure that the user has an entry in /etc/passwd
current_uid=$(id -u)
declare -r current_uid
if ! grep -q "${current_uid}" /etc/passwd ; then
    # Adding it here to avoid panics in the automation agent
    sed -e "s/^mongodb:/builder:/" /etc/passwd > /tmp/passwd
    echo "mongodb:x:$(id -u):$(id -g):,,,:/mongodb-automation:/bin/bash" >> /tmp/passwd
    export LD_PRELOAD=libnss_wrapper.so
    export NSS_WRAPPER_PASSWD=/tmp/passwd
    export NSS_WRAPPER_GROUP=/etc/group
fi

# Create a symlink, after the volumes have been mounted
# If the journal directory already exists (this could be the migration of the existing MongoDB database) - we need
# to copy it to the correct location first and remove a directory
if [[ -d /data/journal ]] && [[ ! -L /data/journal ]]; then
    script_log "The journal directory /data/journal already exists - moving its content to /journal"
    if [[ $(find /data/journal -maxdepth 1 | wc -l) -gt 0 ]]; then
        mv /data/journal/* /journal
    fi
    rm -rf /data/journal
fi

ln -sf /journal /data/
script_log "Created symlink: /data/journal -> $(readlink -f /data/journal)"

# If it is a migration of the existing MongoDB - then there could be a mongodb.log in a default location -
# let's try to copy it to a new directory
if [[ -f /data/mongodb.log ]] && [[ ! -f "${MDB_LOG_FILE_MONGODB}" ]]; then
    script_log "The mongodb log file /data/mongodb.log already exists - moving it to ${MMS_LOG_DIR}"
    mv /data/mongodb.log "${MMS_LOG_DIR}"
fi

base_url="${BASE_URL-}" # If unassigned, set to empty string to avoid set-u errors
base_url="${base_url%/}" # Remove any accidentally defined trailing slashes
declare -r base_url

# Download the Automation Agent from Ops Manager
# Note, that it will be skipped if the agent is supposed to be run in headless mode
if [[ -n "${base_url}" ]]; then
    download_agent
fi

# Start the Automation Agent
agentOpts=(
    "-mmsGroupId" "${GROUP_ID-}"
    "-pidfilepath" "${MMS_HOME}/mongodb-mms-automation-agent.pid"
    "-maxLogFileDurationHrs" "24"
    "-logLevel" "${LOG_LEVEL:-INFO}"
)
AGENT_VERSION="$(cat "${MMS_HOME}"/files/agent-version)"
script_log "Automation Agent version: ${AGENT_VERSION}"

# in multi-cluster mode we need to override the hostname with which, agents
# registers itself, use service FQDN instead of POD FQDN, this mapping is mounted into
# the pod using configmap
hostpath="$(hostname)"

# We apply the ephemeralPortOffset when using externalDomain in Single Cluster
# or whenever Multi-Cluster is on.
override_file="/opt/scripts/config/${hostpath}"
if [[ -f "${override_file}" ]]; then
  override="$(cat "$override_file")"
  agentOpts+=("-overrideLocalHost" "${override}")
  agentOpts+=("-ephemeralPortOffset" "1")
elif [ "${MULTI_CLUSTER_MODE-}" = "true" ]; then
  agentOpts+=("-ephemeralPortOffset" "1")
fi

agentOpts+=("-healthCheckFilePath" "${MMS_LOG_DIR}/agent-health-status.json")
agentOpts+=("-useLocalMongoDbTools")

if [[ -n "${base_url}" ]]; then
    agentOpts+=("-mmsBaseUrl" "${base_url}")
else
    agentOpts+=("-cluster" "${cluster_config_file}")
    # we need to open the web server on localhost even though we don't use it - otherwise Agent doesn't
    # produce status information at all (we need it in health file)
    agentOpts+=("-serveStatusPort" "5000")
    script_log "Mongodb Agent is configured to run in \"headless\" mode using local config file"
fi

if [[ -n "${HTTP_PROXY-}" ]]; then
    agentOpts+=("-httpProxy" "${HTTP_PROXY}")
fi

if [[ -n "${SSL_TRUSTED_MMS_SERVER_CERTIFICATE-}" ]]; then
    agentOpts+=("-httpsCAFile" "${SSL_TRUSTED_MMS_SERVER_CERTIFICATE}")
fi

if [[ "${SSL_REQUIRE_VALID_MMS_CERTIFICATES-}" != "false" ]]; then
    # Only set this option when valid certs are required. The default is false
    agentOpts+=("-tlsRequireValidMMSServerCertificates")
fi

# we can't directly use readarray as this bash version doesn't support
# the delimiter option
splittedAgentFlags=();
while read -rd,; do
    splittedAgentFlags+=("$REPLY")
done <<<"$AGENT_FLAGS";

AGENT_API_KEY="$(cat "${MMS_HOME}"/agent-api-key/agentApiKey)"
script_log "Launching automation agent with following arguments: ${agentOpts[*]} -mmsApiKey ${AGENT_API_KEY+<hidden>} ${splittedAgentFlags[*]}"

agentOpts+=("-mmsApiKey" "${AGENT_API_KEY-}")

rm /tmp/mongodb-mms-automation-cluster-backup.json &> /dev/null || true

debug="${MDB_AGENT_DEBUG-}"
if [ "${debug}" = "true" ]; then
  export PATH=$PATH:/var/lib/mongodb-mms-automation/gopath/bin
  dlv --headless=true --listen=:5006 --accept-multiclient=true --continue --api-version=2 exec "${MMS_HOME}/files/mongodb-mms-automation-agent" -- "${agentOpts[@]}" "${splittedAgentFlags[@]}" 2>> "${MDB_LOG_FILE_AUTOMATION_AGENT_STDERR}" > >(json_log "automation-agent-stdout") &
else
# Note, that we do logging in subshell - this allows us to save the correct PID to variable (not the logging one)
  "${MMS_HOME}/files/mongodb-mms-automation-agent" "${agentOpts[@]}" "${splittedAgentFlags[@]}" 2>> "${MDB_LOG_FILE_AUTOMATION_AGENT_STDERR}" >> >(json_log "automation-agent-stdout") &
fi
export agentPid=$!
script_log "Launched automation agent, pid=${agentPid}"

trap cleanup SIGTERM

wait
