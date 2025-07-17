#!/bin/bash
# Dummy probe script that returns success until real probe is copied
# This prevents container startup failures during probe script copying phase
echo "Using dummy probe - waiting for real probe script to be copied"
exit 0
