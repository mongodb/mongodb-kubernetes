#!/usr/bin/env bash

# The "e" switch cannot be used here. There are certain commands, such as type or command in the agent-launcher-lib.sh
# that return "1" as valid case.
set -Eou pipefail

MDB_STATIC_CONTAINERS_ARCHITECTURE="${MDB_STATIC_CONTAINERS_ARCHITECTURE:-}"
MMS_HOME=${MMS_HOME:-/mongodb-automation}
MMS_LOG_DIR=${MMS_LOG_DIR:-/var/log/mongodb-mms-automation}

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
tail -F -n0 "${MDB_LOG_FILE_AUTOMATION_AGENT_STDERR}" 2> /dev/null | json_log 'automation-agent-stderr' &
tail -F -n0 "${MDB_LOG_FILE_AUTOMATION_AGENT}" 2> /dev/null | json_log 'automation-agent' &
tail -F -n0 "${MDB_LOG_FILE_MONITORING_AGENT}" 2> /dev/null | json_log 'monitoring-agent' &
tail -F -n0 "${MDB_LOG_FILE_BACKUP_AGENT}" 2> /dev/null | json_log 'backup-agent' &
tail -F -n0 "${MDB_LOG_FILE_MONGODB}" 2> /dev/null | json_log 'mongodb' &
tail -F -n0 "${MDB_LOG_FILE_MONGODB_AUDIT}" 2> /dev/null | json_log 'mongodb-audit' &

# This is the directory corresponding to 'options.downloadBase' in the automation config - the directory where
# the agent will extract MongoDB binaries to
mdb_downloads_dir="/var/lib/mongodb-mms-automation"

# The path to the automation config file in case the agent is run in headless mode
cluster_config_file="/var/lib/mongodb-automation/cluster-config.json"

# file required by Automation Agents of authentication is enabled.
touch "${mdb_downloads_dir}/keyfile"
chmod 600 "${mdb_downloads_dir}/keyfile"

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
  if [[ -n "${base_url}" ]]; then
      download_agent
  fi
fi

# Start the Automation Agent
agentOpts=(
    "-mmsGroupId=${GROUP_ID-}"
    "-pidfilepath=${MMS_HOME}/mongodb-mms-automation-agent.pid"
    "-maxLogFileDurationHrs=24"
    "-logLevel=${LOG_LEVEL:-INFO}"
)

# in multi-cluster mode we need to override the hostname with which, agents
# registers itself, use service FQDN instead of POD FQDN, this mapping is mounted into
# the pod using configmap
hostpath="$(hostname)"

# We apply the ephemeralPortOffset when using externalDomain in Single Cluster
# or whenever Multi-Cluster is on.
override_file="/opt/scripts/config/${hostpath}"
if [[ -f "${override_file}" ]]; then
  override="$(cat "${override_file}")"
  agentOpts+=("-overrideLocalHost=${override}")
  agentOpts+=("-ephemeralPortOffset=1")
elif [ "${MULTI_CLUSTER_MODE-}" = "true" ]; then
  agentOpts+=("-ephemeralPortOffset=1")
fi

agentOpts+=("-healthCheckFilePath=${MMS_LOG_DIR}/agent-health-status.json")
if [ -z "${MDB_STATIC_CONTAINERS_ARCHITECTURE}" ]; then
  agentOpts+=("-useLocalMongoDbTools=true")
else
  agentOpts+=("-operatorMode=true")
fi


if [[ -n "${base_url}" ]]; then
    agentOpts+=("-mmsBaseUrl=${base_url}")
else
    agentOpts+=("-cluster=${cluster_config_file}")
    # we need to open the web server on localhost even though we don't use it - otherwise Agent doesn't
    # produce status information at all (we need it in health file)
    agentOpts+=("-serveStatusPort=5000")
    script_log "Mongodb Agent is configured to run in \"headless\" mode using local config file"
fi



if [[ -n "${HTTP_PROXY-}" ]]; then
    agentOpts+=("-httpProxy=${HTTP_PROXY}")
fi

if [[ -n "${SSL_TRUSTED_MMS_SERVER_CERTIFICATE-}" ]]; then
    agentOpts+=("-httpsCAFile=${SSL_TRUSTED_MMS_SERVER_CERTIFICATE}")
fi

if [[ "${SSL_REQUIRE_VALID_MMS_CERTIFICATES-}" != "false" ]]; then
    # Only set this option when valid certs are required. The default is false
    agentOpts+=("-tlsRequireValidMMSServerCertificates=true")
else
    agentOpts+=("-tlsRequireValidMMSServerCertificates=false")
fi

# we can't directly use readarray as this bash version doesn't support
# the delimiter option
splittedAgentFlags=();
while read -rd,; do
    splittedAgentFlags+=("${REPLY}")
done <<<"${AGENT_FLAGS}";

AGENT_API_KEY="$(cat "${MMS_HOME}"/agent-api-key/agentApiKey)"
script_log "Launching automation agent with following arguments: ${agentOpts[*]} -mmsApiKey=${AGENT_API_KEY+<hidden>} ${splittedAgentFlags[*]}"

agentOpts+=("-mmsApiKey=${AGENT_API_KEY-}")

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
    mkdir -p "${mdb_downloads_dir}/mongod"
    mkdir -p "${mdb_downloads_dir}/mongod/bin"
    ln -sf "${MONGOD_ROOT}/bin/mongo" ${mdb_downloads_dir}/mongod/bin/mongo
    ln -sf "${MONGOD_ROOT}/bin/mongod" ${mdb_downloads_dir}/mongod/bin/mongod
    ln -sf "${MONGOD_ROOT}/bin/mongos" ${mdb_downloads_dir}/mongod/bin/mongos

    ln -sf "/tools/mongodump" ${mdb_downloads_dir}/mongod/bin/mongodump
    ln -sf "/tools/mongorestore" ${mdb_downloads_dir}/mongod/bin/mongorestore
    ln -sf "/tools/mongoexport" ${mdb_downloads_dir}/mongod/bin/mongoexport
    ln -sf "/tools/mongoimport" ${mdb_downloads_dir}/mongod/bin/mongoimport
  else
    echo "Mongod PID not found within the specified time."
    exit 1
  fi

  agentOpts+=("-binariesFixedPath=${mdb_downloads_dir}/mongod/bin")
fi

debug="${MDB_AGENT_DEBUG-}"
if [ "${debug}" = "true" ]; then
  cd ${mdb_downloads_dir} || true
  mkdir -p /var/lib/mongodb-mms-automation/gopath
  mkdir -p /var/lib/mongodb-mms-automation/go
  curl -LO https://go.dev/dl/go1.20.1.linux-amd64.tar.gz
  tar -xzf go1.20.1.linux-amd64.tar.gz
  export GOPATH=${mdb_downloads_dir}/gopath
  export GOCACHE=${mdb_downloads_dir}/.cache
  export PATH=${PATH}:${mdb_downloads_dir}/go/bin
  export PATH=${PATH}:${mdb_downloads_dir}/gopath/bin
  go install github.com/go-delve/delve/cmd/dlv@latest
  export PATH=${PATH}:${mdb_downloads_dir}/gopath/bin
  cd ${mdb_downloads_dir} || true
  dlv --headless=true --listen=:5006 --accept-multiclient=true --continue --api-version=2 exec "${AGENT_BINARY_PATH}" -- "${agentOpts[@]}" "${splittedAgentFlags[@]}" 2>> "${MDB_LOG_FILE_AUTOMATION_AGENT_STDERR}" > >(json_log "automation-agent-stdout") &
else
# Note, that we do logging in subshell - this allows us to save the correct PID to variable (not the logging one)
  "${AGENT_BINARY_PATH}" "${agentOpts[@]}" "${splittedAgentFlags[@]}" 2>> "${MDB_LOG_FILE_AUTOMATION_AGENT_STDERR}" >> >(json_log "automation-agent-stdout") &
fi

export agentPid=$!
script_log "Launched automation agent, pid=${agentPid}"

trap cleanup SIGTERM

wait
