#!/usr/bin/env bash

set -euo pipefail

echo "Updating configuration properties file ${mms_prop_file}"

# Update the properties file used to start ops manager.
# They are read from system properties and must have the prefix "OM_PROP_"
# Note, that as bash identifiers cannot have dots - all properties names have "_" instead

# "!" allows to get all variables names by prefix
for var in "${!OM_PROP_@}"; do
    mmsProperty="${var//OM_PROP_/}"
    mmsProperty="${mmsProperty//_/.}"
    if grep -q ${mmsProperty} ${mms_prop_file}; then
        # deleting the line instead of substituting new property
        # because there are issues with sed and special characters e.g. in mongoUri
        # "-i.${1}.bak" allows to create a backup configuration file
        sed -i.bak "/${mmsProperty}=.*$/d" ${mms_prop_file}
    fi
    line="${mmsProperty}=${!var}"
    echo "Using property ${line}"
    echo ${line} >> ${mms_prop_file}
done

# todo seems some properties in mms.conf can also be updated (jvm Xmx for example) depending on the configuration
# we can follow the same approach as for conf.properties - just use another prefix

echo "Starting Ops Manager..."

# only start Ops Manager
/etc/init.d/mongodb-mms start_mms
log_dir="/opt/mongodb/mms/logs"
tail -F "${log_dir}/mms0.log" "${log_dir}/mms0-startup.log" "${log_dir}/mms0-access.log" "${log_dir}/mms-migration.log"
