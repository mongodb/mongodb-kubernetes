#!/usr/bin/env bash
set -o nounset
set -o errexit
set -o pipefail

# Ensure that the user has an entry in /etc/passwd
current_uid=$(id -u)
declare -r current_uid
if ! grep -q "${current_uid}" /etc/passwd ; then
    # Adding it here to avoid panics in the automation agent
    sed -e "s/^mongodb:/builder:/" /etc/passwd > /tmp/passwd
    echo "mongodb:x:$(id -u):$(id -g):,,,:/mongodb-automation:/bin/bash" >> /tmp/passwd
    cat /tmp/passwd > /etc/passwd
    rm /tmp/passwd

    echo "Added ${current_uid} to /etc/passwd"
fi

# Create a symlink to make sure it happens after the volumes have been mounted
ln -s /journal /data/journal
echo "Created symlink: /data/journal -> $(readlink -f /data/journal)"

base_url="${BASE_URL-}" # If unassigned, set to empty string to avoid set-u errors
base_url="${base_url%/}" # Remove any accidentally defined trailing slashes
declare -r base_url

# Download the Automation Agent from Ops Manager
if [ ! -e "${MMS_HOME}/files/mongodb-mms-automation-agent" ]; then
    echo "Downloading an Automation Agent from ${base_url}"
    echo
    pushd /tmp >/dev/null
    curl --silent --retry 3 --fail -o automation-agent.tar.gz "${base_url}/download/agent/automation/mongodb-mms-automation-agent-latest.linux_x86_64.tar.gz"
    tar -xzf automation-agent.tar.gz
    mv mongodb-mms-automation-agent-*/mongodb-mms-automation-agent "${MMS_HOME}/files/"
    chmod +x "${MMS_HOME}/files/mongodb-mms-automation-agent"
    rm -rf automation-agent.tar.gz mongodb-mms-automation-agent-*.linux_x86_64
    echo "The Automation Agent was deployed at ${MMS_HOME}/files/mongodb-mms-automation-agent"
    echo
    popd >/dev/null
fi

# Start the Automation Agent
if [ -e "${MMS_HOME}/mongodb-mms-automation-agent.pid" ]; then
    # Already running
    pid=$(cat "${MMS_HOME}/mongodb-mms-automation-agent.pid")
    echo
    echo "-- The Automation Agent is already running on pid=${pid}!"
    echo
else
    # Start the agent
    echo "-- Launching automation agent with following arguments:"
    echo "    -mmsBaseUrl '${base_url}'"
    echo "    -mmsGroupId '${GROUP_ID-}'"
    echo "    -mmsApiKey '${AGENT_API_KEY+<hidden>}'" # Do not display AGENT_API_KEY

    agentOpts=(
        "-mmsBaseUrl" "${base_url}"
        "-mmsGroupId" "${GROUP_ID-}"
        "-mmsApiKey" "${AGENT_API_KEY-}"
        "-pidfilepath" "${MMS_HOME}/mongodb-mms-automation-agent.pid"
        "-logLevel" "DEBUG"
        "-logFile" "${MMS_LOG_DIR}/automation-agent.log"
    )
    if [ ! -z "${HTTP_PROXY-}" ]; then
        agentOpts+=("-httpProxy" "${HTTP_PROXY}")
        echo "    -httpProxy '${HTTP_PROXY}'"
    fi
    "${MMS_HOME}/files/mongodb-mms-automation-agent" "${agentOpts[@]}" 2>> "${MMS_LOG_DIR}/automation-agent-stderr.log" &
fi

echo
echo "Waiting until logs are created..."
while [ ! -f "${MMS_LOG_DIR}/automation-agent.log" ] && [ ! -f "${MMS_LOG_DIR}/automation-agent-stderr.log" ]; do
    sleep 1
done

echo
echo "Automation Agent logs:"
tail -n 1000 -F "${MMS_LOG_DIR}/automation-agent.log" "${MMS_LOG_DIR}/automation-agent-stderr.log" 2>/dev/null
