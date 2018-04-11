#!/usr/bin/env bash

mms_home=/mongodb-automation
mms_log_dir=/var/log/mongodb-mms-automation

if [ -e $mms_home/mongodb-mms-automation-agent.pid ]; then
    echo "-- Automation agent is running"
else
    echo "-- Launching automation agent with following arguments:
    -mmsBaseUrl $BASE_URL
    -mmsGroupId $GROUP_ID"

    if [ -z $AGENT_API_KEY ]; then
        echo "-mmsApiKey (not specified)"
    else
        echo "-mmsApiKey <hidden>"

    $mms_home/mongodb-mms-automation-agent \
        -mmsBaseUrl $BASE_URL \
        -mmsGroupId $GROUP_ID \
        -mmsApiKey $AGENT_API_KEY \
        -pidfilepath $mms_home/mongodb-mms-automation-agent.pid \
        -logLevel DEBUG \
        -logFile $mms_log_dir/automation-agent.log \
         >> $mms_log_dir/automation-agent-fatal.log 2>&1 &
fi

echo "-- Reading automation agent log file forever"
# todo seems this doesn't work correctly and isn't streamed to supervisor output
tail -F $mms_log_dir/automation-agent.log