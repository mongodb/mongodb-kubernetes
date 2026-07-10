#!/usr/bin/env bash
#
# Run a single E2E test (or marker) locally against the kind cluster.
#
# Usage: e2e_run.sh <marker_or_path>
#
# Examples:
#   e2e_run.sh e2e_search_replicaset_external_mongodb_multi_mongot_managed_lb
#   e2e_run.sh tests/search/search_replicaset_external_mongodb_multi_mongot_managed_lb.py
#
# Logs to logs/test-<sanitized-name>-<timestamp>.log.
# Assumes the operator is already running (via op_run.sh).
#

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

if [[ $# -ne 1 ]]; then
  sed -n '3,15p' "$0"
  exit 1
fi
target="$1"

root="$(git rev-parse --show-toplevel 2>/dev/null || echo /workspace)"
cd "${root}"

# shellcheck disable=SC1091
source scripts/dev/set_env_context.sh

mkdir -p logs
sanitized="${target//[^A-Za-z0-9._-]/_}"
log_path="logs/test-${sanitized}-$(date +%Y%m%d-%H%M%S).log"
# Stable symlink to the latest test log — the devcontainer tmuxp pane
# tails this path so it picks up new runs automatically (matches the
# logs/operator.log convention from op_run.sh).
ln -sf "${log_path#logs/}" logs/test.log

# --junitxml at the repo-root logs/ path the EVG `upload_e2e_logs` function's
# attach.xunit_results expects; the on-host devc variant runs pytest directly
# (no e2e-test pod) so this is the only producer of myreport.xml.
pytest_args=(-v -s "--junitxml=${root}/logs/myreport.xml")
# Treat anything that looks like a path as a positional, otherwise use -m.
if [[ "${target}" == */* || "${target}" == *.py ]]; then
  pytest_args+=("../../${target}")
else
  pytest_args+=(-m "${target}")
fi

cd docker/mongodb-kubernetes-tests
echo "Running: pytest ${pytest_args[*]}"
echo "Log: ${root}/${log_path}"
exec pytest "${pytest_args[@]}" 2>&1 | tee "${root}/${log_path}"
