#!/usr/bin/env bash

# see if jq is available for json logging
use_jq="$(command -v jq)"

# log stdout as structured json with given log type
json_log() {
    if [ "${use_jq}" ]; then
        jq --unbuffered --null-input -c --raw-input "inputs | {\"logType\": \"$1\", \"contents\": .}"
    else
    echo "$1"
    fi
}

# log a given message in json format
script_log() {
    echo "$1" | json_log 'agent-launcher-script' &>>"${MDB_LOG_FILE_AGENT_LAUNCHER_SCRIPT}"
}

# the function reacting on SIGTERM command sent by the container on its shutdown. Makes sure all processes started (including
# mongodb) receive the signal. For MongoDB this results in graceful shutdown of replication (starting from 4.0.9) which may
# take some time. The script waits for all the processes to finish, otherwise the container would terminate as Kubernetes
# waits only for the process with pid #1 to end
cleanup() {
    # Important! Keep this in sync with DefaultPodTerminationPeriodSeconds constant from constants.go
    termination_timeout_seconds=600

    script_log "Caught SIGTERM signal. Passing the signal to the automation agent and the mongod processes."

    kill -15 "${agentPid:?}"
    wait "${agentPid}"

    mongoPid="$(cat /data/mongod.lock)"

    if [ -n "${mongoPid}" ]; then
      kill -15 "${mongoPid}"

      script_log "Waiting until mongod process is shutdown. Note, that if mongod process fails to shutdown in the time specified by the 'terminationGracePeriodSeconds' property (default ${termination_timeout_seconds} seconds) then the container will be killed by Kubernetes."

      # dev note: we cannot use 'wait' for the external processes, seems the spinning loop is the best option
      while [ -e "/proc/${mongoPid}" ]; do sleep 0.1; done
    fi

    script_log "Mongod and automation agent processes are shutdown"
}

# ensure_certs_symlinks function checks if certificates and CAs are mounted and creates symlinks to them
ensure_certs_symlinks() {
    # the paths inside the pod. Move to parameters if multiple usage is needed
    secrets_dir="/var/lib/mongodb-automation/secrets"
    custom_ca_dir="${secrets_dir}/ca"
    pod_secrets_dir="/mongodb-automation"

    if [ -d "${secrets_dir}/certs" ]; then
        script_log "Found certificates in the host, will symlink to where the automation agent expects them to be"
        podname=$(hostname)

        if [ ! -f "${secrets_dir}/certs/${podname}-pem" ]; then
            script_log "PEM Certificate file does not exist in ${secrets_dir}/certs/${podname}-pem. Check the Secret object with certificates is well formed."
            exit 1
        fi

        ln -sf "${secrets_dir}/certs/${podname}-pem" "${pod_secrets_dir}/server.pem"
    fi

    if [ -d "${custom_ca_dir}" ]; then
        if [ -f "${custom_ca_dir}/ca-pem" ]; then
            script_log "Using CA file provided by user"
            ln -sf "${custom_ca_dir}/ca-pem" "${pod_secrets_dir}/ca.pem"
        else
            script_log "Could not find CA file. The name of the entry on the Secret object should be 'ca-pem'"
            exit 1
        fi
    else
        script_log "Using Kubernetes CA file"

        if [[ ! -f "${pod_secrets_dir}/ca.pem" ]]; then
          ln -sf "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt" "${pod_secrets_dir}/ca.pem"
        fi
    fi
}

# download_agent function downloads and unpacks the Mongodb Agent
download_agent() {
    pushd /tmp >/dev/null || true


    if [[ -z "${MDB_AGENT_VERSION-}" ]]; then
      AGENT_VERSION="latest"
    else
      AGENT_VERSION="${MDB_AGENT_VERSION}"
    fi

    # Check if custom agent URL is provided
    if [[ -n "${MDB_CUSTOM_AGENT_URL-}" ]]; then
        script_log "Using custom agent URL: ${MDB_CUSTOM_AGENT_URL}"
        curl_opts=(
            "${MDB_CUSTOM_AGENT_URL}"
            "--location" "--silent" "--retry" "3" "--fail" "-v"
            "--output" "automation-agent.tar.gz"
        );
        script_log "Downloading a Mongodb Agent via ${curl_opts[0]:?}"
    else
        # Detect architecture for agent download
        local detected_arch
        detected_arch=$(uname -m)

        case "${detected_arch}" in
            x86_64)
                AGENT_FILE="mongodb-mms-automation-agent-${AGENT_VERSION}.linux_x86_64.tar.gz"
                ;;
            aarch64|arm64)
                AGENT_FILE="mongodb-mms-automation-agent-${AGENT_VERSION}.amzn2_aarch64.tar.gz"
                ;;
            ppc64le)
                AGENT_FILE="mongodb-mms-automation-agent-${AGENT_VERSION}.rhel8_ppc64le.tar.gz"
                ;;
            s390x)
                AGENT_FILE="mongodb-mms-automation-agent-${AGENT_VERSION}.rhel7_s390x.tar.gz"
                ;;
            *)
                script_log "Error: Unsupported architecture for MongoDB agent: ${detected_arch}"
                exit 1
                ;;
        esac

        script_log "Downloading Agent version: ${AGENT_VERSION}"
        curl_opts=(
            "${base_url}/download/agent/automation/${AGENT_FILE}"

            "--location" "--silent" "--retry" "3" "--fail" "-v"
            "--output" "automation-agent.tar.gz"
        );
        script_log "Downloading a Mongodb Agent via ${curl_opts[0]:?}"
    fi


    if [ "${SSL_REQUIRE_VALID_MMS_CERTIFICATES-}" = "false" ]; then
        # If we are not expecting valid certs, `curl` should be run with `--insecure` option.
        # The default is NOT to accept insecure connections.
        curl_opts+=("--insecure")
    fi

    if [ -n "${SSL_TRUSTED_MMS_SERVER_CERTIFICATE-}" ]; then
        curl_opts+=("--cacert" "${SSL_TRUSTED_MMS_SERVER_CERTIFICATE}")
    fi

    if ! curl "${curl_opts[@]}" &>"${MMS_LOG_DIR}/curl.log"; then
        script_log "Error while downloading the Mongodb agent"
        exit 1
    fi
    json_log 'agent-launcher-script' <"${MMS_LOG_DIR}/curl.log" >>"${MDB_LOG_FILE_AGENT_LAUNCHER_SCRIPT}"
    rm "${MMS_LOG_DIR}/curl.log" 2>/dev/null || true

    script_log "The Mongodb Agent binary downloaded, unpacking"

    mkdir -p "${MMS_HOME}/files"
    tar -xzf automation-agent.tar.gz
    AGENT_VERSION=$(find . -name "mongodb-mms-automation-agent-*" | awk -F"-" '{ print $5 }')
    mv mongodb-mms-automation-agent-*/mongodb-mms-automation-agent "${MMS_HOME}/files/"
    rm -rf automation-agent.tar.gz mongodb-mms-automation-agent-*.*

    echo "${AGENT_VERSION}" >"${MMS_HOME}/files/agent-version"
    chmod +x "${MMS_HOME}/files/mongodb-mms-automation-agent"
    script_log "The Automation Agent was deployed at ${MMS_HOME}/files/mongodb-mms-automation-agent"

    popd >/dev/null || true
}
