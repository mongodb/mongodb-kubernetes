#!/usr/bin/env bash
#
# Run a single E2E test (or marker) locally against the kind cluster.
#
# Usage: e2e_run.sh [--detach] <marker_or_path>
#
# Examples:
#   e2e_run.sh e2e_search_replicaset_external_mongodb_multi_mongot_managed_lb
#   e2e_run.sh tests/search/search_replicaset_external_mongodb_multi_mongot_managed_lb.py
#   e2e_run.sh --detach e2e_search_connectivity_tool_mc_rs
#
# Logs to logs/test-<sanitized-name>-<timestamp>.log (stable symlink:
# logs/test.log). pytest's exit code is written to logs/test.exit on
# completion — with --detach, poll it from the host with `wt-ctl wait --test`.
# Assumes the operator is already running (op_run.sh; `wt-ctl create` starts
# it as its final phase).
#

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

detach=0
foreground=0
targets=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --detach) detach=1; shift ;;
    --foreground) foreground=1; shift ;;  # internal: the detached child
    -h|--help) sed -n '3,17p' "$0"; exit 0 ;;
    --) shift; while [[ $# -gt 0 ]]; do targets+=("$1"); shift; done ;;
    *) targets+=("$1"); shift ;;
  esac
done
if [[ ${#targets[@]} -ne 1 ]]; then
  sed -n '3,17p' "$0"
  exit 1
fi
target="${targets[0]}"

root="$(git rev-parse --show-toplevel 2>/dev/null || echo /workspace)"
cd "${root}"

# shellcheck disable=SC1091
source scripts/dev/set_env_context.sh

mkdir -p logs
rm -f logs/test.exit logs/test.pid

if [[ ${detach} -eq 1 && ${foreground} -eq 0 ]]; then
  nohup "$0" --foreground "${target}" >/dev/null 2>&1 &
  echo "$!" > logs/test.pid
  echo "Detached: pid $(cat logs/test.pid)"
  echo "Log:  logs/test.log   (host: <worktree>/logs/test.log)"
  echo "Exit: logs/test.exit (pytest's exit code, written on completion)"
  echo "Wait: wt-ctl wait --test   (from the host, in the worktree)"
  exit 0
fi

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

# `pytest ... | tee` hides pytest's exit code behind tee's (the final
# pipeline element). Disable errexit for the pipeline and recover
# PIPESTATUS[0] so the caller (and logs/test.exit) sees the real verdict.
set +e
pytest "${pytest_args[@]}" 2>&1 | tee "${root}/${log_path}"
rc=${PIPESTATUS[0]}
set -e

echo "${rc}" > "${root}/logs/test.exit"
exit "${rc}"
