#!/usr/bin/env bash
set -Eeou pipefail

if ! command -v ct &> /dev/null; then
  echo "Error: 'ct' command not found in PATH. Please download it from here https://github.com/helm/chart-testing" >&2
  exit 1
fi

source scripts/dev/set_env_context.sh

if [ -z "${PROJECT_DIR}" ]; then
  echo "Error: PROJECT_DIR environment variable is not set. Please set a context or set it (PROJECT_DIR var) to your local MCK repo manually." >&2
  exit 1
fi

export PATH=${PROJECT_DIR}/venv/bin:${PATH}

ct lint --charts="${PROJECT_DIR}/helm_chart/" \
    --chart-yaml-schema "${PROJECT_DIR}/helm_chart/tests/schemas/chart_schema.yaml" \
    --lint-conf "${PROJECT_DIR}/helm_chart/tests/schemas/lintconf.yaml"
