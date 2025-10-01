#!/usr/bin/env bash
set -Eeou pipefail

source scripts/dev/set_env_context.sh

if ! command -v ct &> /dev/null; then
  echo "Error: 'ct' command not found in PATH. Please download it from here https://github.com/helm/chart-testing" >&2
  exit 1
fi

if [ -z "${PROJECT_DIR}" ]; then
  echo "Error: PROJECT_DIR environment variable is not set. Please set a context or set it (PROJECT_DIR var) to your local MCK repo manually." >&2
  exit 1
fi

# the binaries yamale and yamllint required by ct are available at `${PROJECT_DIR}/venv/bin`
export PATH=${PROJECT_DIR}/venv/bin:${PATH}

helm template "${PROJECT_DIR}/helm_chart/" \
  -f "${PROJECT_DIR}/helm_chart/values.yaml" \
  -f "${PROJECT_DIR}/helm_chart/values-openshift.yaml" \
  -f "${PROJECT_DIR}/helm_chart/values-multi-cluster.yaml"

ct lint --charts="${PROJECT_DIR}/helm_chart/" \
    --chart-yaml-schema "${PROJECT_DIR}/helm_chart/tests/schemas/chart_schema.yaml" \
    --lint-conf "${PROJECT_DIR}/helm_chart/tests/schemas/lintconf.yaml"
