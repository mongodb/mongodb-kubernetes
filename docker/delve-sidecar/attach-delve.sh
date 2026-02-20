#!/bin/bash
set -e

echo "Waiting for application process..."
while ! pgrep -f "mongodb-mms-automation-agent" > /dev/null; do
  echo "App not found, retrying in 5s..."
  sleep 5
done

APP_PID=$(pgrep -f "mongodb-mms-automation-agent")
echo "Found app with PID: ${APP_PID}, attaching Delve..."

dlv attach "${APP_PID}" --headless --listen=:2345 --api-version=2 --accept-multiclient --continue
