#!/bin/bash
# Dummy readiness probe that returns success until real probe is copied
# This prevents container startup failures during probe script copying phase
echo "Using dummy readiness probe - waiting for real probe script to be copied"
exit 0
