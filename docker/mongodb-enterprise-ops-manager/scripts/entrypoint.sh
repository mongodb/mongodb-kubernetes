#!/bin/sh
set -o errexit

echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]: entrypoint.sh"

# This was taken from https://blog.openshift.com/jupyter-on-openshift-part-6-running-as-an-assigned-user-id/
# to avoid uids with no name (issue present in OpenShift).
if [ "$(id -u)" -ge 10000 ]; then
    # Assume this user does not have an entry in /etc/{group,passwd} so we create one here.
    # In OpenShift the $(id -u) will be something easy to identify, like 1000290000, but it changes
    # from deployment to deployment.
    echo "mongodb-mms-runner:x:$(id -u):0:/:/bin/false" >> /etc/passwd
    echo "mongodb-mms-runner:x:$(id -g)" >> /etc/group

    echo "/etc/password is"
    cat /etc/passwd

    echo "/etc/group is"
    cat /etc/group
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
# echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]: Starting BackupDB..."
# "${mongodb}/mongod" --port 27018 --dbpath /data/backup --logpath "${log_dir}/mongod-backup.log" --wiredTigerCacheSizeGB 0.5 --fork

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
    # wait a few seconds for Ops Manager to be ready to handle http connections
    sleep 20
    echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]: Configuring Ops Manager / registering a Global Owner..."
    . /opt/venv/bin/activate
    # if we fail here it might be because we already initialized this image, no need to do it again.
    # Also, make sure the ".ops-manager-env" file resides in a directory that is restored after a restart of the Pod
    # like a PersistentVolume, or this file won't be found
    OM_ENV_FILE="/opt/mongodb/mms/env/.ops-manager-env"
    echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]: Credentials to be stored in ${OM_ENV_FILE}"
    /opt/scripts/configure-ops-manager.py "http://${OM_HOST}:${OM_PORT}" "${OM_ENV_FILE}" || true
    # keep going if a user has registered already, we'll assume it is us.
fi
echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]: Ops Manager ready..."

# Tail all Ops Manager and MongoD log files
tail -F "${log_dir}/mms0.log" "${log_dir}/mms0-startup.log" "${log_dir}/daemon.log" "${log_dir}/daemon-startup.log" "${log_dir}/mongod-appdb.log" "${log_dir}/mongod-backup.log"
