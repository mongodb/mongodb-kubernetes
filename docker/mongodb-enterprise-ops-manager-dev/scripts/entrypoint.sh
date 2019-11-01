#!/bin/sh
set -o errexit

echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]: entrypoint.sh"

if [ "$(id -u)" -ge 10000 ]; then
    cp /etc/passwd /tmp/passwd
    cp /etc/group /tmp/group
    echo "mongodb-mms-runner:x:$(id -u):0:/:/bin/false" >> /tmp/passwd
    echo "mongodb-mms-runner:x:$(id -g)" >> /tmp/group
    export LD_PRELOAD=libnss_wrapper.so
    export NSS_WRAPPER_PASSWD=/tmp/passwd
    export NSS_WRAPPER_GROUP=/tmp/group
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
    # TODO check the port in the loop
    # smth like while ! timeout 1 bash -c "echo > /dev/tcp/localhost/${OM_PORT}"; do  sleep 1; done
    sleep 30
    echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]: Configuring Ops Manager / registering a Global Owner..."
    . /opt/venv/bin/activate
    # if we fail here it might be because we already initialized this image, no need to do it again.
    # Also, make sure the ".ops-manager-env" file resides in a directory that is restored after a restart of the Pod
    # like a PersistentVolume, or this file won't be found
    OM_ENV_FILE="/opt/mongodb/mms/env/.ops-manager-env"
    echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]: Credentials to be stored in ${OM_ENV_FILE}"

    /opt/scripts/configure-ops-manager.py "http://${OM_HOST}:${OM_PORT}" "${OM_ENV_FILE}" || true

    if [ ! -f "${OM_ENV_FILE}" ]; then
        echo "The env file ${OM_ENV_FILE} doesn't exist - exiting!"
        exit 1
    fi
    # keep going if a user has registered already, we'll assume it is us.
fi
echo "[$(date -u +'%Y-%m-%dT%H:%M:%SZ')]: Ops Manager ready..."

# Tail all Ops Manager and MongoD log files
tail -F "${om_log_dir}/mms0.log" "${om_log_dir}/mms0-startup.log" "${log_dir}/daemon.log" "${log_dir}/daemon-startup.log" "${log_dir}/mongod-appdb.log" "${log_dir}/mongod-backup.log"
