#!/usr/bin/env bash

set -Eeou pipefail

scripts/dev/run_python.sh scripts/release/pipeline_main.py --parallel agent \
    --all-agents \
    --build-scenario manual_release \
    -r quay.io/mongodb/mongodb-agent
