#!/usr/bin/env bash

set -euo pipefail

# we need to change the Home directory for current bash so that the gen key was found correctly
# (the key is searched in "${HOME}/.mongodb-mms/gen.key")
HOME=${MMS_HOME}

# Update the properties file used to start ops manager.
# They are read from system properties and must have the prefix "OM_PROP_"
# Note, that as bash identifiers cannot have dots - all properties names have "_" instead
echo "Updating configuration properties file ${MMS_PROP_FILE}"

# "!" allows to get all variables names by prefix
for var in "${!OM_PROP_@}"; do
    mmsProperty="${var//OM_PROP_/}"
    mmsProperty="${mmsProperty//_/.}"
    if grep -q ${mmsProperty} ${MMS_PROP_FILE}; then
        # deleting the line instead of substituting new property
        # because there are issues with sed and special characters e.g. in mongoUri
        # "-i.${1}.bak" allows to create a backup configuration file
        sed -i.bak "/${mmsProperty}=.*$/d" ${MMS_PROP_FILE}
    fi
    line="${mmsProperty}=${!var}"
    echo "Using property ${line}"
    echo ${line} >> ${MMS_PROP_FILE}
done

# todo seems some properties in mms.conf can also be updated (jvm Xmx for example) depending on the configuration
# we can follow the same approach as for conf.properties - just use another prefix

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

    tail -F -n 1000 "${MMS_LOG_DIR}/mms0.log" "${MMS_LOG_DIR}/mms0-startup.log" "${MMS_LOG_DIR}/mms-migration.log"
else
    echo "Starting Ops Manager Backup Daemon"
    ${MMS_HOME}/bin/mongodb-mms start_backup_daemon

    tail -F "${MMS_LOG_DIR}/daemon.log"
fi
