#!/bin/bash
set -Eeou pipefail

# current copy of docker/mongodb-enterprise-database/content
# FIXME: remove the scripts from docker/mongodb-enterprise-database/content once database builds also uses multi-stage builds

agent_pid=/mongodb-automation/mongodb-mms-automation-agent.pid

check_agent_pid () {
    # the agent PID must exists always
    # it it does not exists, we assume it is being updated
    # so we have a failure threshold of a few minutes.
    [ -f $agent_pid ]
}

baby_container () {
    # returns 0 if host's uptime is less than 1 hour
    # To check if container uptime is less than 1 hour,
    # we check for how long the pid1 process has
    # been running.
    pid1_alive_secs=$(ps -o etimes= -p 1)
    pid1_alive_mins=$((pid1_alive_secs / 60))

    [ $pid1_alive_mins -lt 60 ]
}

check_mongod_alive () {
    pgrep --exact 'mongod'
}

check_mongos_alive () {
    pgrep --exact 'mongos'
}

check_mongo_process_alive () {
    # the mongod process pid might not always exist
    # 1. when the container is being created the mongod package needs to be
    #    downloaded. the agent will wait for 1 hour before giving up.
    # 2. the mongod process might be getting updated, we'll set a
    #    failureThreshold on the livenessProbe to a few minutes before we
    #    give up.

    baby_container || check_mongod_alive || check_mongos_alive
}

check_agent_pid && check_mongo_process_alive
