#!/usr/bin/env bash

# The "e" switch cannot be used here. There are certain commands, such as type or command in the agent-launcher-lib.sh
# that return "1" as valid case.
set -Eou pipefail

MDB_STATIC_CONTAINERS_ARCHITECTURE="${MDB_STATIC_CONTAINERS_ARCHITECTURE:-}"
MMS_HOME=${MMS_HOME:-/mongodb-automation}
MMS_LOG_DIR=${MMS_LOG_DIR:-/var/log/mongodb-mms-automation}

# This is the directory corresponding to 'options.downloadBase' in the automation config - the directory where
# the agent will extract MongoDB binaries to
MMS_DOWNLOAD_BASE=${MMS_DOWNLOAD_BASE:-/var/lib/mongodb-mms-automation}

if [ -z "${MDB_STATIC_CONTAINERS_ARCHITECTURE}" ]; then
  AGENT_BINARY_PATH="${MMS_HOME}/files/mongodb-mms-automation-agent"
else
  AGENT_BINARY_PATH="/agent/mongodb-agent"
fi

export MDB_LOG_FILE_AGENT_LAUNCHER_SCRIPT="${MMS_LOG_DIR}/agent-launcher-script.log"

# We start tailing script logs immediately to not miss anything.
# -F flag is equivalent to --follow=name --retry.
# -n0 parameter is instructing tail to show only new lines (by default tail is showing last 10 lines)
tail -F -n0 "${MDB_LOG_FILE_AGENT_LAUNCHER_SCRIPT}" 2> /dev/null &

source /opt/scripts/agent-launcher-lib.sh

# all the following MDB_LOG_FILE_* env var should be defined in container's env vars
tail -F -n0 "${MDB_LOG_FILE_AUTOMATION_AGENT_VERBOSE}" 2> /dev/null | json_log 'automation-agent-verbose' &
tail -F -n0 "${MDB_LOG_FILE_AUTOMATION_AGENT}" 2> /dev/null | json_log 'automation-agent' &
tail -F -n0 "${MDB_LOG_FILE_MONITORING_AGENT}" 2> /dev/null | json_log 'monitoring-agent' &
tail -F -n0 "${MDB_LOG_FILE_BACKUP_AGENT}" 2> /dev/null | json_log 'backup-agent' &
tail -F -n0 "${MDB_LOG_FILE_MONGODB}" 2> /dev/null | json_log 'mongodb' &
tail -F -n0 "${MDB_LOG_FILE_MONGODB_AUDIT}" 2> /dev/null | json_log 'mongodb-audit' &

# file required by Automation Agents of authentication is enabled.
touch "${MMS_DOWNLOAD_BASE}/keyfile"
chmod 600 "${MMS_DOWNLOAD_BASE}/keyfile"

ensure_certs_symlinks

# Ensure that the user has an entry in /etc/passwd
current_uid=$(id -u)
declare -r current_uid
if ! grep -q "${current_uid}" /etc/passwd ; then
    # Adding it here to avoid panics in the automation agent
    sed -e "s/^mongodb:/builder:/" /etc/passwd > /tmp/passwd
    echo "mongodb:x:$(id -u):$(id -g):,,,:/mongodb-automation:/bin/bash" >> /tmp/passwd
    export LD_PRELOAD=libnss_wrapper.so
    export NSS_WRAPPER_PASSWD=/tmp/passwd
    export NSS_WRAPPER_GROUP=/etc/group
fi

# Create a symlink, after the volumes have been mounted
# If the journal directory already exists (this could be the migration of the existing MongoDB database) - we need
# to copy it to the correct location first and remove a directory
if [[ -d /data/journal ]] && [[ ! -L /data/journal ]]; then
    script_log "The journal directory /data/journal already exists - moving its content to /journal"

    # Check if /journal is not empty, if so, empty it
    if [[ $(ls -A /journal) && ${MDB_CLEAN_JOURNAL:-1} -eq 1 ]]; then
       # we can only create a dir under tmp, since its read only
        MDB_JOURNAL_BACKUP_DIR="/tmp/journal_backup_$(date +%Y%m%d%H%M%S)"
        script_log "The /journal directory is not empty - moving its content to ${MDB_JOURNAL_BACKUP_DIR}"
        mkdir -p "${MDB_JOURNAL_BACKUP_DIR}"
        mv /journal/* "${MDB_JOURNAL_BACKUP_DIR}"
    fi


    if [[ $(find /data/journal -maxdepth 1 | wc -l) -gt 0 ]]; then
        mv /data/journal/* /journal
    fi

    rm -rf /data/journal
fi

ln -sf /journal /data/
script_log "Created symlink: /data/journal -> $(readlink -f /data/journal)"

# If it is a migration of the existing MongoDB - then there could be a mongodb.log in a default location -
# let's try to copy it to a new directory
if [[ -f /data/mongodb.log ]] && [[ ! -f "${MDB_LOG_FILE_MONGODB}" ]]; then
    script_log "The mongodb log file /data/mongodb.log already exists - moving it to ${MMS_LOG_DIR}"
    mv /data/mongodb.log "${MMS_LOG_DIR}"
fi

base_url="${BASE_URL-}" # If unassigned, set to empty string to avoid set-u errors
base_url="${base_url%/}" # Remove any accidentally defined trailing slashes
declare -r base_url

if [ -z "${MDB_STATIC_CONTAINERS_ARCHITECTURE}" ]; then
  # Download the Automation Agent from Ops Manager
  # Note, that it will be skipped if the agent is supposed to be run in headless mode
  # unless a custom agent URL is provided, in which case we download it regardless.
  if [[ -n "${base_url}" ]] || [[ -n "${MDB_CUSTOM_AGENT_URL:-}" ]]; then
      download_agent
  fi
fi

agentOpts=(-f /etc/mongodb-agent/automation-agent.conf)

# The hostname ConfigMap maps each pod name to its advertised service or external hostname.
override_file="/opt/scripts/config/$(hostname)"
if [[ ${MDB_AGENT_HOSTNAME_OVERRIDE+x} ]]; then
  agentOpts+=("-overrideLocalHost=${MDB_AGENT_HOSTNAME_OVERRIDE}")
elif [[ -f "${override_file}" ]]; then
  override="$(cat "${override_file}")"
  agentOpts+=("-overrideLocalHost=${override}")
fi

# Remove stale agent health status file if exists.
#
# With `spec.persistent = true` the `{MMS_LOG_DIR}` directory is mounted using PVC. That means during pod recreation
# we are not losing any logs. At the same time `agent-health-status.json` file is also is preserved during restarts.
# This is problematic, because our readiness probe uses this file as source of truth for deployment status
# and if it is stale we can quickly mark the container as ready, while in fact it is still booting up.
rm -f "${MMS_LOG_DIR}/agent-health-status.json" 2>/dev/null || true

# We never set the -httpProxy flag.
# Without the flag, the agent relies solely on standard environment variables (HTTP_PROXY, HTTPS_PROXY, NO_PROXY).
# This avoids conflicts between environment settings and agent CLI parameters.
# For reference, see the agent implementation:
# https://github.com/10gen/mms-automation/blob/19f44a18cc089ec3734e2b496fdde82b124cd945/go_planner/src/com.tengen/cm/backup/commonbackup/connections.go#L158

rm /tmp/mongodb-mms-automation-cluster-backup.json &> /dev/null || true

if [ -z "${MDB_STATIC_CONTAINERS_ARCHITECTURE}" ]; then
  echo "Skipping creating symlinks because this is not Static Containers Architecture"
else
  WAIT_TIME=5
  MAX_WAIT=300
  ELAPSED_TIME=0

  # Polling loop to wait for the PID value to be non-zero
  while [ ${ELAPSED_TIME} -lt ${MAX_WAIT} ]; do
    script_log "waiting for mongod_pid being available"
    # shellcheck disable=SC2009
    MONGOD_PID=$(ps aux | grep "mongodb_marker" | grep -v grep | awk '{print $2}') || true

    if [ -n "${MONGOD_PID}" ] && [ "${MONGOD_PID}" -ne 0 ]; then
    break
    fi

    sleep ${WAIT_TIME}
    ELAPSED_TIME=$((ELAPSED_TIME + WAIT_TIME))
  done

  # Check if a non-zero PID value is found
  if [ -n "${MONGOD_PID}" ] && [ "${MONGOD_PID}" -ne 0 ]; then
    echo "Mongod PID: ${MONGOD_PID}"
    MONGOD_ROOT="/proc/${MONGOD_PID}/root"
    mkdir -p "${MMS_DOWNLOAD_BASE}/mongod"
    mkdir -p "${MMS_DOWNLOAD_BASE}/mongod/bin"
    ln -sf "${MONGOD_ROOT}/bin/mongo" "${MMS_DOWNLOAD_BASE}/mongod/bin/mongo"
    ln -sf "${MONGOD_ROOT}/bin/mongod" "${MMS_DOWNLOAD_BASE}/mongod/bin/mongod"
    ln -sf "${MONGOD_ROOT}/bin/mongos" "${MMS_DOWNLOAD_BASE}/mongod/bin/mongos"

    for tool in mongoimport mongodump mongorestore mongoexport; do
      [ -e "/tools/${tool}" ] || { echo "/tools/${tool} not found"; exit 1; }
      ln -sf "/tools/${tool}" "${MMS_DOWNLOAD_BASE}/mongod/bin/${tool}"
    done
  else
    echo "Mongod PID not found within the specified time."
    exit 1
  fi

fi

script_log "Launching automation agent"

# Note, that we do logging in subshell - this allows us to save the correct PID to variable (not the logging one)
"${AGENT_BINARY_PATH}" "${agentOpts[@]}" 2> >(json_log "automation-agent-stderr") >> >(json_log "automation-agent-stdout") &

export agentPid=$!
script_log "Launched automation agent, pid=${agentPid}"

trap cleanup SIGTERM

wait
