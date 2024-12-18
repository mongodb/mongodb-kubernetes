#!/usr/bin/env bash

set -Eeou pipefail
source scripts/dev/set_env_context.sh

go install github.com/google/go-licenses@v1.6.0

# Define the root of the repo and the scripts directory
REPO_DIR=$(dirname "$(dirname "$(dirname "$(readlink -f "$0")")")")
SCRIPTS_DIR=$(dirname "$(readlink -f "$0")")

# Function to process licenses in a given directory
process_licenses() {
    local DIR="$1"

    echo "Processing licenses for module: $DIR"

    if ! cd "$DIR"; then
        echo "Failed to change directory to $DIR"
        return 1
    fi

    PATH=$GOPATH/bin:$PATH GOOS=linux GOARCH=amd64 go-licenses report . --template "$SCRIPTS_DIR/update_licenses.tpl" > licenses_full.csv 2> licenses_stderr  || true

    # Filter and sort the licenses report
    grep -v 10gen licenses_full.csv | grep -v "github.com/mongodb" | grep -v "^golang.org" | sort > licenses.csv || true

    # Return to the repo root directory
    cd "$REPO_DIR" || exit
}

process_licenses "$REPO_DIR" &
process_licenses "$REPO_DIR/public/tools/multicluster" &

wait

echo "License processing complete for all modules."
