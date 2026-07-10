#!/usr/bin/env bash
#
# Start the operator locally (go run ./main.go) inside a tmux session named
# 'mck-operator' so it survives the devcontainer-exec call that started it.
# Logs stream to logs/operator-<timestamp>.log; logs/operator.log is a
# stable symlink to the latest run.
#
# Modes:
#   (no args)  Start + wait for ready + tail the log. Ctrl-C stops the
#              operator (kills the mck-operator tmux session) and exits.
#   --detach   Start + wait for ready + return. Operator keeps running
#              in the mck-operator session. Use this from scripted
#              callers that need to know "operator is up" before
#              proceeding (e.g. before launching e2e_run.sh).
#

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

mode="tail"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --detach) mode="detach"; shift ;;
    -h|--help) sed -n '3,15p' "$0"; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; exit 1 ;;
  esac
done

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo /workspace)"

# Source the per-side env via the canonical bootstrap. devenv handles:
#   - side detection via /.dockerenv
#   - logical .generated/context.env + site .generated/context.<side>.env
#   - venv activation if present
# Operator-specific overlay (.generated/context.operator.env) is loaded
# by main.go's loadEnvFromLocalFileForDevelopment, not by this shell.
# shellcheck disable=SC1091
. scripts/dev/devenv

mkdir -p logs
log_path="logs/operator-$(date +%Y%m%d-%H%M%S).log"
ln -sf "${log_path#logs/}" logs/operator.log

# Operator binds health/metrics on 8181 and webhook on 9443. Wipe stale ports
# so a previous unclean exit doesn't crash this run with EADDRINUSE.
for port in 8181 9443; do
  pid="$(lsof -ti:"${port}" 2>/dev/null || true)"
  if [[ -n "${pid}" ]]; then
    echo "killing stale pid ${pid} on port ${port}"
    kill -9 "${pid}" 2>/dev/null || true
  fi
done

# Multi-cluster mode requires the operator to watch the mongodbmulticluster
# CRD; otherwise no MongoDBMultiCluster controller starts and the CR is
# silently ignored. The --watch-resource flag is repeatable; passing any
# of them disables the default list, so we have to enumerate the full set.
operator_args=""
proxy_env=""
if [[ "${KUBE_ENVIRONMENT_NAME:-}" == "multi" ]]; then
  operator_args="--watch-resource=mongodb \
--watch-resource=mongodbusers \
--watch-resource=opsmanagers \
--watch-resource=mongodbcommunity \
--watch-resource=mongodbsearch \
--watch-resource=clustermongodbroles \
--watch-resource=mongodbmulticluster"

  # The operator's memberwatch healthcheck reads MULTI_CLUSTER_HEALTHCHECK_PROXY
  # explicitly (not the standard HTTP_PROXY env vars, because Go hardcodes a
  # 127.0.0.1/localhost bypass that overrides NO_PROXY — and the member kind
  # API servers ARE on 127.0.0.1:<port> on the EVG host's loopback, reachable
  # from the devcontainer only via gost-proxy → SSH tunnel).
  if [[ -n "${EVG_HOST_PROXY:-}" ]]; then
    proxy_env="MULTI_CLUSTER_HEALTHCHECK_PROXY=${EVG_HOST_PROXY}"
  fi
fi

tmux kill-session -t mck-operator 2>/dev/null || true
tmux new-session -d -s mck-operator \
  "bash -lc 'cd \"$(pwd)\"; . scripts/dev/devenv; ${proxy_env} go run ./main.go ${operator_args} 2>&1 | tee ${log_path}'"

echo "Operator started in tmux session 'mck-operator'."
echo "  log: ${log_path}  (symlinked as logs/operator.log)"
echo "  attach: tmux a -t mck-operator"

# Both modes wait for the operator to be ready first.
echo -n "Waiting for operator to start "
ready=0
for _ in $(seq 1 90); do
  if grep -qE 'Starting workers|webhook .* listening|Starting EventSource|controller=.* started' "${log_path}" 2>/dev/null; then
    echo
    echo "Operator is up."
    ready=1
    break
  fi
  if grep -qE 'panic:|fatal:|level=error' "${log_path}" 2>/dev/null; then
    echo
    echo "Operator log shows error:"
    tail -n 40 "${log_path}"
    exit 1
  fi
  echo -n "."
  sleep 1
done

if [[ ${ready} -eq 0 ]]; then
  echo
  echo "Timed out waiting for operator readiness."
  tail -n 40 "${log_path}"
  exit 1
fi

if [[ ${mode} == detach ]]; then
  exit 0
fi

# Tail mode: stream the log and, on Ctrl-C / SIGTERM, stop the operator.
# bash defers pending signals while waiting on a foreground child, so
# after tail dies on SIGINT bash runs this trap before exiting.
trap '
  echo
  echo "[op_run] stopping operator (tmux session mck-operator)..."
  tmux kill-session -t mck-operator 2>/dev/null || true
  exit 0
' INT TERM

echo
echo "[op_run] Tailing ${log_path}. Ctrl-C to stop the operator."
echo
tail -F "${log_path}"
