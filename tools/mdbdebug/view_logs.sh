#!/usr/bin/env bash

set -Eeou pipefail

if [ $# -ne 1 ]; then
    echo "Usage: $0 <directory or tar.gz file>"
    exit 1
fi

input_path="$1"
log_dir=""

# Check if the input is a tar.gz file
if [[ "$input_path" == *.tar.gz ]]; then
    temp_dir=$(mktemp -d)
    echo "Extracting tar.gz to $temp_dir"
    tar -xzf "$input_path" -C "$temp_dir"
    
    # Verify the expected directory exists after extraction
    if [ -d "$temp_dir/mongodb-mms-automation" ]; then
        log_dir="$temp_dir"
        echo "Extracted logs directory: $log_dir/mongodb-mms-automation"
    else
        echo "Error: The tar.gz file does not contain the expected directory structure"
        rm -rf "$temp_dir"
        exit 1
    fi
else
    # Input is a directory
    if [ -d "$input_path" ]; then
        log_dir=$(dirname "$input_path")
        base_dir=$(basename "$input_path")
        if [ "$base_dir" != "mongodb-mms-automation" ]; then
            echo "Warning: Directory name is not 'mongodb-mms-automation' as expected"
        fi
    else
        echo "Error: $input_path is not a valid directory or tar.gz file"
        exit 1
    fi
fi

# Run docker container with the logs directory mounted
docker run --rm -it \
    -v "$log_dir/mongodb-mms-automation:/var/log/mongodb-mms-automation" \
    quay.io/lsierant/diffwatch:latest

# Clean up temp directory if we created one
if [[ "$input_path" == *.tar.gz ]]; then
    rm -rf "$temp_dir"
    echo "Removed temporary directory"
fi

