#!/usr/bin/env bash


# Assuring assigned uid has an entry in /etc/passwd
# This was taken from https://blog.openshift.com/jupyter-on-openshift-part-6-running-as-an-assigned-user-id/
# to avoid uids with no name (issue present in OpenShift).
if [ `id -u` -ge 10000 ]; then
    cat /etc/passwd | sed -e "s/^mongodb:/builder:/" > /tmp/passwd
    echo "mongodb:x:`id -u`:`id -g`:,,,:/mongodb-automation:/bin/bash" >> /tmp/passwd
    cat /tmp/passwd > /etc/passwd
    rm /tmp/passwd
fi

mms_home=/mongodb-automation
mms_log_dir=/var/log/mongodb-mms-automation

if [ -e $mms_home/mongodb-mms-automation-agent.pid ]; then
    echo "-- Automation agent is running"
else
    echo "-- Launching automation agent with following arguments:
    -mmsBaseUrl $BASE_URL
    -mmsGroupId $GROUP_ID"

    if [ -z $AGENT_API_KEY ]; then
        echo "    -mmsApiKey (not specified)"
    else
        echo "    -mmsApiKey <hidden>"
    fi

    $mms_home/files/mongodb-mms-automation-agent \
        -mmsBaseUrl $BASE_URL \
        -mmsGroupId $GROUP_ID \
        -mmsApiKey $AGENT_API_KEY \
        -pidfilepath $mms_home/mongodb-mms-automation-agent.pid \
        -logLevel DEBUG \
        -logFile $mms_log_dir/automation-agent.log \
             2>> $mms_log_dir/automation-agent-stderr.log &
fi

# Waiting for some time until log file appears
sleep 5

tail -n 1000 -F $mms_log_dir/automation-agent.log $mms_log_dir/automation-agent-stderr.log
