#!/usr/bin/env bash

# Finds the exec_timeout_secs for this task in Evergreen. If it can't find it, will return the general
# exec_timeout_secs attribute set in top-level evergreen.yaml.
get_timeout_for_task () {
    local task_name=${1}

    local exec_timeout
    # OMG: really???
    exec_timeout=$(grep "name: ${task_name}$" .evergreen.yml -A 3 | grep exec_timeout_secs | awk '{ print $2 }')
    if [[ ${exec_timeout} = "" ]]; then
        exec_timeout=$(grep "^exec_timeout_secs:" .evergreen.yml | head -1 | awk '{ print $2 }')
    fi

    echo "${exec_timeout}"
}

# Returns a random string that can be used as a namespace.
generate_random_namespace() {
    local random_namespace
    random_namespace=$(LC_ALL=C tr -dc 'a-z0-9' </dev/urandom | head -c 10)
    local seconds_epoch
    seconds_epoch=$(date +'%s')
    echo "a-${seconds_epoch}-${random_namespace}z"
}
