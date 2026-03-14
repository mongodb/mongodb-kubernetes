#!/bin/bash

# Function to retry a command with a configurable delay
# Usage: retry_cmd "command to execute" delay_seconds
retry_cmd() {
    local cmd="$1"
    local delay="${2:-3}"
    
    while true; do
        eval "$cmd"
        echo "Retrying..."
        sleep "$delay"
    done
}