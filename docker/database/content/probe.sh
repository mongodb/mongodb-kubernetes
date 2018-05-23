#!/bin/bash


agent_pid=/mongodb-automation/mongodb-mms-automation-agent.pid
mongod_pid=/data/mongod.lock

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

check_mongod_pid () {
    # The mongod pid might not always exists, specially when the container
    # has been recently created and the automation agent is still
    # downloading.
    [ -f $mongod_pid ]
}

check_mongod_alive () {
    # the mongod pid might not always exist
    # 1. when the container is being created the mongod package needs to be
    #    downloaded. the agent will wait for 1 hour before giving up.
    # 2. the mongod process might be getting updated, we'll set a
    #    failureThreshold on the livenessProbe to a few minutes before we
    #    give up.

    baby_container || check_mongod_pid
}

check_agent_pid && check_mongod_alive
