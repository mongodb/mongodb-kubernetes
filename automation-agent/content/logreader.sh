#!/usr/bin/env bash

mms_home=/mongodb-automation

if [ -e $mms_home/mongodb-mms-automation-agent.pid ]; then
    echo "-- Automation agent is running"
else
    $mms_home/mongodb-mms-automation-agent \
        -mmsBaseUrl $BASE_URL \
        -mmsGroupId $GROUP_ID \
        -mmsApiKey $AGENT_API_KEY \
        -pidfilepath $mms_home/mongodb-mms-automation-agent.pid \
        -logFile $mms_home/automation-agent.log &
fi

echo "-- Reading automation agent log file forever"
tail -F $mms_home/automation-agent.log
