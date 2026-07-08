#!/usr/bin/env bash

# The "e" switch cannot be used here. There are certain commands, such as type or command in the agent-launcher-lib.sh
# that return "1" as valid case.
set -Eou pipefail

MDB_STATIC_CONTAINERS_ARCHITECTURE="${MDB_STATIC_CONTAINERS_ARCHITECTURE:-}"
MMS_HOME=${MMS_HOME:-/mongodb-automation}
MMS_LOG_DIR=${MMS_LOG_DIR:-/var/log/mongodb-mms-automation}

# mongod logs to ${MMS_LOG_DIR}/mongod-stdout FIFO; a jq reader drains it tagged to stdout.
# Under ShareProcessNamespace (static arch) PID 1 is the pause container and
# /proc/1/fd/1 is /dev/null, but FIFOs don't depend on /proc (POSIX pipes).
# $$ resolves to 1 in non static and in static to launcher pid.
mkdir -p "${MMS_LOG_DIR}"
mkfifo "${MMS_LOG_DIR}/mongod-stdout"
jq --unbuffered -Rc --arg p mongod '{process:$p,msg:.}' < "${MMS_LOG_DIR}/mongod-stdout" &

if [ -z "${MDB_STATIC_CONTAINERS_ARCHITECTURE}" ]; then
  AGENT_BINARY_PATH="${MMS_HOME}/files/mongodb-mms-automation-agent"
else
  AGENT_BINARY_PATH="/agent/mongodb-agent"
fi

source /opt/scripts/agent-launcher-lib.sh

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

# Remove stale agent health status file if exists.
#
# With `spec.persistent = true` the `{MMS_LOG_DIR}` directory is mounted using PVC. That means during pod recreation
# we are not losing any logs. At the same time `agent-health-status.json` file is also is preserved during restarts.
# This is problematic, because our readiness probe uses this file as source of truth for deployment status
# and if it is stale we can quickly mark the container as ready, while in fact it is still booting up.
rm -f "${MMS_LOG_DIR}/agent-health-status.json" 2>/dev/null || true

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

# We never set the -httpProxy flag.
# Without the flag, the agent relies solely on standard environment variables (HTTP_PROXY, HTTPS_PROXY, NO_PROXY).
# This avoids conflicts between environment settings and agent CLI parameters.
# For reference, see the agent implementation:
# https://github.com/10gen/mms-automation/blob/19f44a18cc089ec3734e2b496fdde82b124cd945/go_planner/src/com.tengen/cm/backup/commonbackup/connections.go#L158

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

    for tool in mongoimport mongodump mongorestore mongoexport; do
      [ -e "/tools/${tool}" ] || { echo "/tools/${tool} not found"; exit 1; }
      ln -sf "/tools/${tool}" ${mdb_downloads_dir}/mongod/bin/${tool}
    done
  else
    echo "Mongod PID not found within the specified time."
    exit 1
  fi

  agentOpts+=("-binariesFixedPath=${mdb_downloads_dir}/mongod/bin")
fi

# Audit log is user-configured. If the user routes audit to a file, tail it to stdout.
tail -F -n0 "${MDB_LOG_FILE_MONGODB_AUDIT:-${MMS_LOG_DIR}/mongodb-audit.log}" 2>/dev/null | jq --unbuffered -Rc --arg p audit '{process:$p,msg:.}' &

# Monitoring/backup goroutines are configured via automation config logPath;
# each writes to its own FIFO, drained tagged to stdout. No log files.
mkfifo "${MMS_LOG_DIR}/monitoring-stdout"
jq --unbuffered -Rc --arg p monitoring '{process:$p,msg:.}' < "${MMS_LOG_DIR}/monitoring-stdout" &
mkfifo "${MMS_LOG_DIR}/backup-stdout"
jq --unbuffered -Rc --arg p backup '{process:$p,msg:.}' < "${MMS_LOG_DIR}/backup-stdout" &

# Run agent stderr/stdout through jq for process tagging.
"${AGENT_BINARY_PATH}" "${agentOpts[@]}" "${splittedAgentFlags[@]}" 2>&1 | jq --unbuffered -Rc --arg p agent '{process:$p,msg:.}' &

export agentPid=$!
script_log "Launched automation agent, pid=${agentPid}"

trap cleanup SIGTERM

wait
