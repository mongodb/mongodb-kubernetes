#!/usr/bin/env bash

set -euo pipefail

# the function reacting on SIGTERM command sent by the container on its shutdown. Redirects the signal
# to the child process ("tail" in this case)
cleanup () {
    echo "Caught SIGTERM signal."
    kill -TERM "$child"
}

# we need to change the Home directory for current bash so that the gen key was found correctly
# (the key is searched in "${HOME}/.mongodb-mms/gen.key")
HOME=${MMS_HOME}

# Execute script that updates properties and conf file used to start ops manager
echo "Updating configuration properties file ${MMS_PROP_FILE} and conf file ${MMS_CONF_FILE}"
/opt/scripts/mmsconfiguration ${MMS_CONF_FILE} ${MMS_PROP_FILE}

if [[ -z ${BACKUP_DAEMON+x} ]]; then
    echo "Starting Ops Manager"
    ${MMS_HOME}/bin/mongodb-mms start_mms || {
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

    trap cleanup SIGTERM
    tail -F -n 1000 "${MMS_LOG_DIR}/mms0.log" "${MMS_LOG_DIR}/mms0-startup.log" "${MMS_LOG_DIR}/mms-migration.log" &
else
    echo "Starting Ops Manager Backup Daemon"
    ${MMS_HOME}/bin/mongodb-mms start_backup_daemon
    trap cleanup SIGTERM

    tail -F "${MMS_LOG_DIR}/daemon.log" &
fi

child=$!
wait "$child"
