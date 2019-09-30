#!/usr/bin/env bash

# This is a file containing all the functions which may be needed for other shell scripts

# log stdout as structured json with given log type
json_log () {
    jq --unbuffered --null-input -c --raw-input "inputs | {\"logType\": \"$1\", \"contents\": .}";
}

# log a given message in json format
script_log () {
    echo "$1" | json_log 'agent-launcher-script'
}

# the function reacting on SIGTERM command sent by the container on its shutdown. Makes sure all processes started (including
# mongodb) receive the signal. For MongoDB this results in graceful shutdown of replication (starting from 4.0.9) which may
# take some time. The script waits for all the processes to finish, otherwise the container would terminate as Kubernetes
# waits only for the process with pid #1 to end
cleanup () {
    # Important! Keep this in sync with DefaultPodTerminationPeriodSeconds constant from constants.go
    termination_timeout_seconds=600

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

# ensure_certs_symlinks function checks if certificates and CAs are mounted and creates symlinks to them
ensure_certs_symlinks () {
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
}

# download_agent function downloads and unpacks the Mongodb Agent
download_agent () {
    script_log "Downloading a Mongodb Agent from ${base_url}"
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
        script_log "Error while downloading the Mongodb agent"
        cat "${MMS_LOG_DIR}/agent-launcher-script.log" | json_log 'agent-launcher-script'
        exit 1
    fi

    script_log "The Mongodb Agent binary downloaded, unpacking"
    tar -xzf automation-agent.tar.gz
    AGENT_VERSION=$(find . -name mongodb-mms-automation-agent-* | awk -F"-" '{ print $5 }')
    echo "${AGENT_VERSION}" > "${MMS_HOME}/files/agent-version"
    mv mongodb-mms-automation-agent-*/mongodb-mms-automation-agent "${MMS_HOME}/files/"
    chmod +x "${MMS_HOME}/files/mongodb-mms-automation-agent"
    rm -rf automation-agent.tar.gz mongodb-mms-automation-agent-*.linux_x86_64
    script_log "The Automation Agent was deployed at ${MMS_HOME}/files/mongodb-mms-automation-agent"
    popd >/dev/null
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
