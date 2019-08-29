#!/usr/bin/env bash
set -o nounset
set -o errexit
set -o pipefail

keyfile_dir="/var/lib/mongodb-mms-automation"
secrets_dir="/var/lib/mongodb-automation/secrets"
pod_secrets_dir="/mongodb-automation"
custom_ca_dir="${secrets_dir}/ca"

# file required by Automation Agents of authentication is enabled.
mkdir -p ${keyfile_dir}
touch "${keyfile_dir}/keyfile"
chmod 600 "${keyfile_dir}/keyfile"

# Important! Keep this in sync with DefaultPodTerminationPeriodSeconds constant from constants.go
termination_timeout_seconds=600

# log stdout as structured json with given log type
function json_log {   jq --unbuffered --null-input -c --raw-input "inputs | {\"logType\": \"$1\", \"contents\": .}"; }

# log a given message in json format
function script_log {
  echo "$1" | json_log 'agent-launcher-script'
}

# the function reacting on SIGTERM command sent by the container on its shutdown. Makes sure all processes started (including
# mongodb) receive the signal. For MongoDB this results in graceful shutdown of replication (starting from 4.0.9) which may
# take some time. The script waits for all the processes to finish, otherwise the container would terminate as Kubernetes
# waits only for the process with pid #1 to end
function cleanup {
  script_log "Caught SIGTERM signal. Passing the signal to the automation agent and the mongod processes."

  kill -15 "$agentPid"
  wait "$agentPid"

  mongoPid="$(cat /data/mongod.lock)"
  kill -15 "$mongoPid"

  script_log "Waiting until mongod process is shutdown. Note, that if mongod process fails to shutdown in the time specified by the 'terminationGracePeriodSeconds' property (default $termination_timeout_seconds seconds) then the container will be killed by Kubernetes."

  # dev note: we cannot use 'wait' for the external processes, seems the spinning loop is the best option
  while [ -e "/proc/$mongoPid" ]; do sleep 0.1; done

  script_log "Mongod and automation agent processes are shutdown"
}

#https://stackoverflow.com/a/4025065/614239
compare_versions () {
    if [[ $1 == $2 ]]
    then
        return 0
    fi
    local IFS=.
    local i ver1=($1) ver2=($2)
    # fill empty fields in ver1 with zeros
    for ((i=${#ver1[@]}; i<${#ver2[@]}; i++))
    do
        ver1[i]=0
    done
    for ((i=0; i<${#ver1[@]}; i++))
    do
        if [[ -z ${ver2[i]} ]]
        then
            # fill empty fields in ver2 with zeros
            ver2[i]=0
        fi
        if ((10#${ver1[i]} > 10#${ver2[i]}))
        then
            return 1
        fi
        if ((10#${ver1[i]} < 10#${ver2[i]}))
        then
            return 2
        fi
    done
    return 0
}

if [ -d "${secrets_dir}/certs" ]; then
    script_log "Found certificates in the host, will symlink to where the automation agent expects them to be"
    podname=$(hostname)

    if [ ! -f "${secrets_dir}/certs/${podname}-pem" ]; then
        script_log "PEM Certificate file does not exist in ${secrets_dir}/certs/${podname}-pem. Check the Secret object with certificates is well formed."
        exit 1
    fi

    ln -s "${secrets_dir}/certs/${podname}-pem" "${pod_secrets_dir}/server.pem"
fi

if [ -d "${custom_ca_dir}" ]; then
    if [ -f "${custom_ca_dir}/ca-pem" ]; then
        script_log "Using CA file provided by user"
        ln -s "${custom_ca_dir}/ca-pem" "${pod_secrets_dir}/ca.pem"
    else
        script_log "Could not find CA file. The name of the entry on the Secret object should be 'ca-pem'"
        exit 1
    fi
else
    script_log "Using Kubernetes CA file"
    ln -s "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt" "${pod_secrets_dir}/ca.pem"
fi

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
# If the journal directory already exists (this could be the migration of the existing MongoDB database) - we need
# to copy it to the correct location first and remove a directory
if [[ -d /data/journal ]] && [[ ! -L /data/journal ]]; then
    script_log "The journal directory /data/journal already exists - moving its content to /journal"
    if [[ $(ls -1 /data/journal | wc -l) -gt 0 ]]; then
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
    mv /data/mongodb.log ${MMS_LOG_DIR}
fi

base_url="${BASE_URL-}" # If unassigned, set to empty string to avoid set-u errors
base_url="${base_url%/}" # Remove any accidentally defined trailing slashes
declare -r base_url

# Download the Automation Agent from Ops Manager
if [ ! -e "${MMS_HOME}/files/mongodb-mms-automation-agent" ]; then
    script_log "Downloading an Automation Agent from ${base_url}"
    pushd /tmp >/dev/null

    curl_opts=(
        "${base_url}/download/agent/automation/mongodb-mms-automation-agent-latest.linux_x86_64.tar.gz"
        "--location" "--silent" "--retry" "3" "--fail" "-v"
        "--output" "automation-agent.tar.gz"
    )

    if [ -n "${SSL_REQUIRE_VALID_MMS_CERTIFICATES-}" ] && [ "${SSL_REQUIRE_VALID_MMS_CERTIFICATES}" = "false" ]; then
        # If we are not expecting valid certs, `curl` should be run with `--insecure` option.
        # The default is NOT to accept insecure connections.
        curl_opts+=("--insecure")
    fi

    if [ -n "${SSL_TRUSTED_MMS_SERVER_CERTIFICATE-}" ]; then
        curl_opts+=("--cacert" "${SSL_TRUSTED_MMS_SERVER_CERTIFICATE}")
    fi

    if ! curl "${curl_opts[@]}" &> "${MMS_LOG_DIR}/agent-launcher-script.log"; then
        script_log "Error while downloading the Automation agent"
        cat "${MMS_LOG_DIR}/agent-launcher-script.log" | json_log 'agent-launcher-script'
        exit 1
    fi

    script_log "The Automation Agent binary downloaded, unpacking"
    tar -xzf automation-agent.tar.gz
    AGENT_VERSION=$(find . -name mongodb-mms-automation-agent-* | awk -F"-" '{ print $5 }')
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
    script_log "Automation Agent version: ${AGENT_VERSION}"
    if [[ -n "${AGENT_VERSION}" ]]; then
        # this is the version of Automation Agent which has fixes for health file bugs
        set +e
        compare_versions "${AGENT_VERSION}" 10.2.3.5866-1
        if [[ $? -le 1 ]]; then
          agentOpts+=("-healthCheckFilePath" "${MMS_LOG_DIR}/agent-health-status.json")
        fi
        set -e
    fi
    if [[ -n "${HTTP_PROXY-}" ]]; then
        agentOpts+=("-httpProxy" "${HTTP_PROXY}")
    fi

    if [[ -n "${SSL_TRUSTED_MMS_SERVER_CERTIFICATE-}" ]]; then
        agentOpts+=("-sslTrustedMMSServerCertificate" "${SSL_TRUSTED_MMS_SERVER_CERTIFICATE}")
    fi

    if [[ -n "${SSL_REQUIRE_VALID_MMS_CERTIFICATES-}" ]] && [[ "${SSL_REQUIRE_VALID_MMS_CERTIFICATES}" = "true" ]]; then
        # Only set this option when valid certs are required. The default is false
        agentOpts+=("-sslRequireValidMMSServerCertificates")
    fi

    script_log "Launching automation agent with following arguments: ${agentOpts[*]} -mmsApiKey ${AGENT_API_KEY+<hidden>}"

    agentOpts+=("-mmsApiKey" "${AGENT_API_KEY-}")

    # Note, that we do logging in subshell - this allows us to save the сorrect PID to variable (not the logging one)
    "${MMS_HOME}/files/mongodb-mms-automation-agent" "${agentOpts[@]}" 2>> "${MMS_LOG_DIR}/automation-agent-stderr.log" > >(json_log "automation-agent-stdout") &
    agentPid=$!

    trap cleanup SIGTERM
fi

# Note that we don't care about orphan processes as they will die together with container in case of any troubles
# tail's -F flag is equivalent to --follow=name --retry. Should we track log rotation events?
tail -F "${MMS_LOG_DIR}/automation-agent-verbose.log" 2> /dev/null | json_log 'automation-agent-verbose' &
tail -F "${MMS_LOG_DIR}/automation-agent-stderr.log" 2> /dev/null | json_log 'automation-agent-stderr' &
tail -F "${MMS_LOG_DIR}/mongodb.log" 2> /dev/null | json_log 'mongodb' &

wait
