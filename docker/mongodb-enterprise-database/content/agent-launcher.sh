#!/usr/bin/env bash
set -o nounset
set -o errexit
set -o pipefail


current_uid=$(id -u)
if ! grep -q "${current_uid}" /etc/passwd ; then
    # User does not have a name in /etc/passwd. Adding it here to avoid panics by the automation agent.
    sed -e "s/^mongodb:/builder:/" /etc/passwd > /tmp/passwd
    echo "mongodb:x:$(id -u):$(id -g):,,,:/mongodb-automation:/bin/bash" >> /tmp/passwd
    cat /tmp/passwd > /etc/passwd
    rm /tmp/passwd

    echo "Added ${current_uid} to /etc/passwd"
fi

mms_home=/mongodb-automation
mms_log_dir=/var/log/mongodb-mms-automation

# we create symlink here to make sure it happens after volumes mounting
ln -s /journal /data/journal
echo "Created symlink: /data/journal -> $(readlink -f /data/journal)"

#shellcheck disable=SC2153
base_url="${BASE_URL%/}" # Remove any accidentally defined trailing slashes

if [ -e "${mms_home}/mongodb-mms-automation-agent.pid" ]; then
    echo "-- Automation agent is running"
else
    echo "-- Launching automation agent with following arguments:
    -mmsBaseUrl ${base_url}
    -mmsGroupId ${GROUP_ID}"

    if [ -z "${AGENT_API_KEY}" ]; then
        echo "    -mmsApiKey (not specified)"
    else
        echo "    -mmsApiKey <hidden>"
    fi

    "${mms_home}/files/mongodb-mms-automation-agent" \
        -mmsBaseUrl "${base_url}" \
        -mmsGroupId "${GROUP_ID}" \
        -mmsApiKey "${AGENT_API_KEY}" \
        -pidfilepath "${mms_home}/mongodb-mms-automation-agent.pid" \
        -logLevel DEBUG \
        -logFile "${mms_log_dir}/automation-agent.log" \
             2>> "${mms_log_dir}/automation-agent-stderr.log" &
fi

echo
echo "Waiting until logs are created..."
while [ ! -f "${mms_log_dir}/automation-agent.log" ] || [ ! -f "${mms_log_dir}/automation-agent-stderr.log" ]; do
    sleep 1
done

echo
echo "Automation Agent logs:"
tail -n 1000 -F "${mms_log_dir}/automation-agent.log" "${mms_log_dir}/automation-agent-stderr.log" 2>/dev/null
