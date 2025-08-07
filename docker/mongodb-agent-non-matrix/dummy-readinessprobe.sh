#!/bin/bash
# Dummy readiness probe that returns NOT READY until real probe is copied
# Container should not be marked ready until real probe scripts are available
echo "Using dummy readiness probe - container not ready until real probe script is copied"
exit 1
