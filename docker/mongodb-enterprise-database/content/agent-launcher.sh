#!/usr/bin/env bash
set -o nounset
set -o errexit
set -o pipefail

# log stdout as structured json with given log type
function json_log {
  jq --unbuffered --null-input --raw-input "inputs | {\"logType\": \"$1\", \"contents\": .}"
}

# log a given message in json format
function script_log {
  echo "$1" | json_log 'agent-launcher-script'
}

# Ensure that the user has an entry in /etc/passwd
current_uid=$(id -u)
declare -r current_uid
if ! grep -q "${current_uid}" /etc/passwd ; then
    # Adding it here to avoid panics in the automation agent
    sed -e "s/^mongodb:/builder:/" /etc/passwd > /tmp/passwd
    echo "mongodb:x:$(id -u):$(id -g):,,,:/mongodb-automation:/bin/bash" >> /tmp/passwd
    cat /tmp/passwd > /etc/passwd
    rm /tmp/passwd

    script_log "Added ${current_uid} to /etc/passwd"
fi

# Create a symlink, after the volumes have been mounted
ln -sf /journal /data/
script_log "Created symlink: /data/journal -> $(readlink -f /data/journal)"

base_url="${BASE_URL-}" # If unassigned, set to empty string to avoid set-u errors
base_url="${base_url%/}" # Remove any accidentally defined trailing slashes
declare -r base_url

# Download the Automation Agent from Ops Manager
if [ ! -e "${MMS_HOME}/files/mongodb-mms-automation-agent" ]; then
    script_log "Downloading an Automation Agent from ${base_url}"
    pushd /tmp >/dev/null
    curl --location --silent --retry 3 --fail -o automation-agent.tar.gz "${base_url}/download/agent/automation/mongodb-mms-automation-agent-latest.linux_x86_64.tar.gz"

    script_log "The Automation Agent binary downloaded, unpacking"
    tar -xzf automation-agent.tar.gz
    mv mongodb-mms-automation-agent-*/mongodb-mms-automation-agent "${MMS_HOME}/files/"
    chmod +x "${MMS_HOME}/files/mongodb-mms-automation-agent"
    rm -rf automation-agent.tar.gz mongodb-mms-automation-agent-*.linux_x86_64
    script_log "The Automation Agent was deployed at ${MMS_HOME}/files/mongodb-mms-automation-agent"
    popd >/dev/null
fi

# Start the Automation Agent
if [ -e "${MMS_HOME}/mongodb-mms-automation-agent.pid" ]; then
    # Already running
    pid=$(cat "${MMS_HOME}/mongodb-mms-automation-agent.pid")
    script_log "The Automation Agent is already running on pid=${pid}!"
else
    # Start the agent
    agentOpts=(
        "-mmsBaseUrl" "${base_url}"
        "-mmsGroupId" "${GROUP_ID-}"
        "-pidfilepath" "${MMS_HOME}/mongodb-mms-automation-agent.pid"
        "-maxLogFileDurationHrs" "24"
        "-logLevel" "${LOG_LEVEL:-INFO}"
        "-logFile" "${MMS_LOG_DIR}/automation-agent.log"
    )
    if [ -n "${HTTP_PROXY-}" ]; then
        agentOpts+=("-httpProxy" "${HTTP_PROXY}")
    fi

    script_log "Launching automation agent with following arguments: ${agentOpts[*]} -mmsApiKey ${AGENT_API_KEY+<hidden>}"

    agentOpts+=("-mmsApiKey" "${AGENT_API_KEY-}")

    "${MMS_HOME}/files/mongodb-mms-automation-agent" "${agentOpts[@]}" 2>> "${MMS_LOG_DIR}/automation-agent-stderr.log" | json_log "automation-agent-stdout" &
fi

# Note that we don't care about orphan processes as they will die together with container in case of any troubles
# tail's -F flag is equivalent to --follow=name --retry. Should we track log rotation events?
tail -F "${MMS_LOG_DIR}/automation-agent-verbose.log" 2> /dev/null | json_log 'automation-agent-verbose' &
tail -F "${MMS_LOG_DIR}/automation-agent-stderr.log" 2> /dev/null | json_log 'automation-agent-stderr' &
tail -F "${MMS_LOG_DIR}/mongodb.log" 2> /dev/null | json_log 'mongodb'
