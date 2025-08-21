#!/usr/bin/env bash

set -Eeou pipefail

scripts/dev/run_python.sh scripts/release/pipeline_main.py --parallel agent \
    --all-agents \
    --build-scenario manual_release \
    -r 268558157000.dkr.ecr.us-east-1.amazonaws.com/lucian.tosa/mongodb-agent
