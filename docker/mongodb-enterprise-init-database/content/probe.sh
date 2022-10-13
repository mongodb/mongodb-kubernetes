#!/bin/bash
set -Eeou pipefail

# current copy of docker/mongodb-enterprise-database/content
# FIXME: remove the scripts from docker/mongodb-enterprise-database/content once database builds also uses multi-stage builds

agent_pid=/mongodb-automation/mongodb-mms-automation-agent.pid
mins=${1:-60}

check_agent_pid() {
    # the agent PID must exists always
    # it it does not exists, we assume it is being updated
    # so we have a failure threshold of a few minutes.
    [ -f $agent_pid ]
}

check_mongod_alive() {
    pgrep --exact 'mongod'
}

check_mongos_alive() {
    pgrep --exact 'mongos'
}

check_mongo_process_alive() {
    # the mongod process pid might not always exist
    # 1. when the container is being created the mongod package needs to be
    #    downloaded. the agent will wait for 1 hour before giving up.
    # 2. the mongod process might be getting updated, we'll set a
    #    failureThreshold on the livenessProbe to a few minutes before we
    #    give up.

    check_mongod_alive || check_mongos_alive
}

# 2 conditions are sufficient to state that a Pod is "Alive":
#
# 1. There is an agent PID present in the filesystem (meaning that the agent is running)
# 2. There is a `mongod` or `mongos` process running
#
check_agent_pid || check_mongo_process_alive
