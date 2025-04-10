#!/usr/bin/env bash

set -Eeou pipefail

# the function reacting on SIGTERM command sent by the container on its shutdown. Redirects the signal
# to the child process ("tail" in this case)
cleanup () {
    echo "Caught SIGTERM signal."
    if [[ -n "${child}" ]]; then
        kill -TERM "${child}"
    else
        # Kill all tail processes
        echo "Was not able to find child process, killing all tail processes"
        pkill -f tail -TERM
    fi
}

# we need to change the Home directory for current bash so that the gen key was found correctly
# (the key is searched in "${HOME}/.mongodb-mms/gen.key")
HOME=${MMS_HOME}
CONFIG_TEMPLATE_DIR=${MMS_HOME}/conf-template
CONFIG_DIR=${MMS_HOME}/conf

if [ -d "${CONFIG_TEMPLATE_DIR}" ]
then
    if [ "$(ls -A "${CONFIG_DIR}")" ]; then
        echo "The ${CONFIG_DIR} directory is not empty. Skipping copying files from ${CONFIG_TEMPLATE_DIR}"
        echo "This might cause errors when booting up the OpsManager with read-only root filesystem"
    else
        echo "Copying ${CONFIG_TEMPLATE_DIR} content to ${CONFIG_DIR}"
        cp "${CONFIG_TEMPLATE_DIR}"/* "${CONFIG_DIR}"
        echo "Done copying ${CONFIG_TEMPLATE_DIR} content to ${CONFIG_DIR}"
    fi
else
    echo "It seems you're running an older version of the Ops Manager image."
    echo "Please pull the latest one."
fi

# Execute script that updates properties and conf file used to start ops manager
echo "Updating configuration properties file ${MMS_PROP_FILE} and conf file ${MMS_CONF_FILE}"
/opt/scripts/mmsconfiguration "${MMS_CONF_FILE}" "${MMS_PROP_FILE}"

if [[ -z ${BACKUP_DAEMON+x} ]]; then
    echo "Starting Ops Manager"
    "${MMS_HOME}/bin/mongodb-mms" start_mms || {
      echo "Startup of Ops Manager failed with code $?"
      if [[ -f ${MMS_LOG_DIR}/mms0-startup.log ]]; then
        echo
        echo "mms0-startup.log:"
        echo
        cat "${MMS_LOG_DIR}/mms0-startup.log"
      fi
      if [[ -f ${MMS_LOG_DIR}/mms0.log ]]; then
        echo
        echo "mms0.log:"
        echo
        cat "${MMS_LOG_DIR}/mms0.log"
      fi
      if [[ -f ${MMS_LOG_DIR}/mms-migration.log ]]; then
        echo
        echo "mms-migration.log"
        echo
        cat "${MMS_LOG_DIR}/mms-migration.log"
      fi
      exit 1
    }

    tail -F -n 1000 "${MMS_LOG_DIR}/mms0.log" "${MMS_LOG_DIR}/mms0-startup.log" "${MMS_LOG_DIR}/mms-migration.log" &
else
    echo "Starting Ops Manager Backup Daemon"
    "${MMS_HOME}/bin/mongodb-mms" start_backup_daemon

    tail -F "${MMS_LOG_DIR}/daemon.log" &
fi

export child=$!
echo "Launched tail, pid=${child}"

trap cleanup SIGTERM

wait "${child}"
