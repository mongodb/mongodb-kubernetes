#!/usr/bin/env bash
set -Eeou pipefail

# shellcheck source=/dev/null
source "${MMS_HOME}/files/agent-launcher-lib.sh"

# The path to the automation config file in case the agent is run in headless mode
cluster_config_file="/var/lib/mongodb-automation/cluster-config.json"

# file required by Automation Agents of authentication is enabled.
keyfile_dir="/var/lib/mongodb-mms-automation"
mkdir -p ${keyfile_dir}
touch "${keyfile_dir}/keyfile"
chmod 600 "${keyfile_dir}/keyfile"

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
if [[ -f /data/mongodb.log ]] && [[ ! -f "${MMS_LOG_DIR}/mongodb.log" ]]; then
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
AGENT_VERSION="$(cat "${MMS_HOME}"/files/agent-version)"

# Start the Automation Agent
agentOpts=(
    "-mmsGroupId" "${GROUP_ID-}"
    "-pidfilepath" "${MMS_HOME}/mongodb-mms-automation-agent.pid"
    "-maxLogFileDurationHrs" "24"
    "-logLevel" "${LOG_LEVEL:-INFO}"
    "-logFile" "${MMS_LOG_DIR}/automation-agent.log"
)
script_log "Automation Agent version: ${AGENT_VERSION}"

# this is the version of Automation Agent which has fixes for health file bugs
set +e
compare_versions "${AGENT_VERSION}" 10.2.3.5866-1
if [[ $? -le 1 ]]; then
  agentOpts+=("-healthCheckFilePath" "${MMS_LOG_DIR}/agent-health-status.json")
fi
set -e

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
    agentOpts+=("-sslTrustedMMSServerCertificate" "${SSL_TRUSTED_MMS_SERVER_CERTIFICATE}")
fi

if [[ "${SSL_REQUIRE_VALID_MMS_CERTIFICATES-}" != "false" ]]; then
    # Only set this option when valid certs are required. The default is false
    agentOpts+=("-sslRequireValidMMSServerCertificates")
fi


script_log "Launching automation agent with following arguments: ${agentOpts[*]} -mmsApiKey ${AGENT_API_KEY+<hidden>} ${AGENT_FLAGS}"

agentOpts+=("-mmsApiKey" "${AGENT_API_KEY-}")

rm /tmp/mongodb-mms-automation-cluster-backup.json || true
# Note, that we do logging in subshell - this allows us to save the сorrect PID to variable (not the logging one)
"${MMS_HOME}/files/mongodb-mms-automation-agent" "${agentOpts[@]}" "${AGENT_FLAGS}" 2>> "${MMS_LOG_DIR}/automation-agent-stderr.log" > >(json_log "automation-agent-stdout") &
export agentPid=$!

trap cleanup SIGTERM

# Note that we don't care about orphan processes as they will die together with container in case of any troubles
# tail's -F flag is equivalent to --follow=name --retry. Should we track log rotation events?
AGENT_VERBOSE_LOG="${MMS_LOG_DIR}/automation-agent-verbose.log" && touch "${AGENT_VERBOSE_LOG}"
AGENT_STDERR_LOG="${MMS_LOG_DIR}/automation-agent-stderr.log" && touch "${AGENT_STDERR_LOG}"
MONGODB_LOG="${MMS_LOG_DIR}/mongodb.log" && touch "${MONGODB_LOG}"

tail -F "${AGENT_VERBOSE_LOG}" 2> /dev/null | json_log 'automation-agent-verbose' &
tail -F "${AGENT_STDERR_LOG}" 2> /dev/null | json_log 'automation-agent-stderr' &
tail -F "${MONGODB_LOG}" 2> /dev/null | json_log 'mongodb' &

wait
