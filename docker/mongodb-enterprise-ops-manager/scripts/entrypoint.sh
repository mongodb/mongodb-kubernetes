#!/bin/sh
set -o errexit

# This was taken from https://blog.openshift.com/jupyter-on-openshift-part-6-running-as-an-assigned-user-id/
# to avoid uids with no name (issue present in OpenShift).
if [ "$(id -u)" -ge 10000 ]; then
    # Ensure that the assigned uid matches the expected user in /etc/passwd
    temp_file=$(mktemp)
    grep -vE "^mongodb-mms:" /etc/passwd                       > "${temp_file}"
    echo "mongodb-mms:x:$(id -u):$(id -g):${HOME}:/bin/false" >> "${temp_file}"
    cat "${temp_file}" > /etc/passwd
    rm "${temp_file}"
    
    # Add the current user into the mongodb-mms group
    temp_file=$(mktemp)
    grep -vE "^mongodb-mms:" /etc/group                        > "${temp_file}"
    sed -r "s/^(mongodb-mms:.*)$/\1$(whoami)/" /etc/group     >> "${temp_file}"
    cat "${temp_file}" > /etc/group
    rm "${temp_file}"

    # TODO(mihaibojin): We might need to also update mongodb-mms user's uid and gid to 10008000, potentially as part of the build process
fi

# Ensure that the required directories exist in /data (needs to be part of runtime, due to /data being mounted as a VOLUME)
mkdir -p /data/appdb
mkdir -p /data/backup
mkdir -p /data/backupDaemon
mkdir -p "${log_dir}"

# Start AppDB
echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]: Starting AppDB..."
"${mongodb}/mongod" --port 27017 --dbpath /data/appdb  --logpath "${log_dir}/mongod-appdb.log"  --wiredTigerCacheSizeGB 0.5 --fork

# Start BackupDB
echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]: Starting BackupDB..."
"${mongodb}/mongod" --port 27018 --dbpath /data/backup --logpath "${log_dir}/mongod-backup.log" --wiredTigerCacheSizeGB 0.5 --fork

# Generate the AppDB encryption key (first run only)

# Replace mms.centralUrl in mms_prop_file (if the OM_HOST environment variable is set)
/opt/scripts/opsman-central-url.sh --clean "${mms_prop_file}"
if [ ! -z "${OM_HOST}" ]; then 
    /opt/scripts/opsman-central-url.sh --set "${mms_prop_file}" "http://${OM_HOST}:${OM_PORT}"
fi

# Run Preflight checks
# Run Migrations
# Start Ops Manager and the Backup Daemon
echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]: Starting Ops Manager..."
/opt/scripts/opsman-initd.sh --start
echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]: Started Ops Manager..."

# Configure Ops Manager (register a global owner, create a project, define a 0/0 whitelist for the public API and retrieve the public API key)
if [ ! -z "${OM_HOST}" ] &&  [ -z "${SKIP_OPS_MANAGER_REGISTRATION}" ]; then
    echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]: Configuring Ops Manager / registering a Global Owner..."
    . /opt/venv/bin/activate
    /opt/scripts/configure-ops-manager.py "http://${OM_HOST}:${OM_PORT}" "/opt/mongodb/mms/.ops-manager-env"
fi
echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]: Ops Manager ready..."

# Tail all Ops Manager and MongoD log files
tail -F "${log_dir}/mms0.log" "${log_dir}/mms0-startup.log" "${log_dir}/daemon.log" "${log_dir}/daemon-startup.log" "${log_dir}/mongod-appdb.log" "${log_dir}/mongod-backup.log"
