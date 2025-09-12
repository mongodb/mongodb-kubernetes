#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

ct lint --charts="${PROJECT_DIR}/helm_chart/" \
    --chart-yaml-schema "${PROJECT_DIR}/helm_chart/tests/schemas/chart_schema.yaml" \
    --lint-conf "${PROJECT_DIR}/helm_chart/tests/schemas/lintconf.yaml"
