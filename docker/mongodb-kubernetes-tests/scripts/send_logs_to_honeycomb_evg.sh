#!/usr/bin/env bash
# Evergreen wrapper for send_logs_to_honeycomb.py.
# Called from upload_e2e_logs via subprocess.exec so HONEYCOMB_API_KEY is
# injected via include_expansions_in_env (not available in shell.exec).
# working_dir in Evergreen is src/github.com/mongodb/mongodb-kubernetes.
set -eo pipefail

TASK_ID="${1:?task_id required}"
VERSION_ID="${2:?version_id required}"

# Activate the repo venv (built from requirements.txt by setup_building_host).
# working_dir is already the project root.
source venv/bin/activate

export PYTHONPATH=".:docker/mongodb-kubernetes-tests"

python docker/mongodb-kubernetes-tests/scripts/send_logs_to_honeycomb.py \
  --logs-dir logs \
  --task-id "${TASK_ID}" \
  --version-id "${VERSION_ID}"
