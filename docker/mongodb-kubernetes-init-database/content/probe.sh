#!/usr/bin/env bash
set -Eeou pipefail

check_process() {
  local check_process=${1}
  # shellcheck disable=SC2009
  ps -ax | grep -v " grep " | grep -v jq | grep -v tail | grep "${check_process}"
  return $?
}

check_agent_alive() {
    check_process 'mongodb-mms-aut'
}

check_mongod_alive() {
  check_process 'mongod'
}

check_mongos_alive() {
    check_process 'mongos'
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

# One of 2 conditions is sufficient to state that a Pod is "Alive":
#
# 1. There is an agent process running
# 2. There is a `mongod` or `mongos` process running
#
check_agent_alive || check_mongo_process_alive
