#!/bin/bash
# Dummy liveness probe that returns success to keep container alive during script copying
# This prevents container from being killed while waiting for real probe scripts
echo "Using dummy liveness probe - keeping container alive until real probe script is copied"
exit 0
