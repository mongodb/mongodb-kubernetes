#!/usr/bin/env bash
#
# Start the operator locally (go run ./main.go) inside a tmux session named
# 'mck-operator' so it survives the devcontainer-exec call that started it.
# Logs stream to logs/operator-<timestamp>.log; logs/operator.log is a
# stable symlink to the latest run.
#
# Usage: op_run.sh [--wait]
#   --wait   Block until the operator log shows it has started.
#

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

wait_for_ready=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --wait) wait_for_ready=1; shift ;;
    -h|--help) sed -n '3,12p' "$0"; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; exit 1 ;;
  esac
done

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo /workspace)"

# Source context. Operator-specific env (CRD-related, search images, etc.)
# lives in context.operator.export.env.
# shellcheck disable=SC1091
set -a
source .generated/context.export.env
source .generated/context.operator.export.env
set +a

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
  "bash -lc 'set -a; source .generated/context.export.env; source .generated/context.operator.export.env; set +a; cd \"$(pwd)\"; ${proxy_env} go run ./main.go ${operator_args} 2>&1 | tee ${log_path}'"

echo "Operator started in tmux session 'mck-operator'."
echo "  log: ${log_path}  (symlinked as logs/operator.log)"
echo "  attach: tmux a -t mck-operator"

if [[ ${wait_for_ready} -eq 1 ]]; then
  echo -n "Waiting for operator to start "
  for _ in $(seq 1 90); do
    if grep -qE 'Starting workers|webhook .* listening|Starting EventSource|controller=.* started' "${log_path}" 2>/dev/null; then
      echo
      echo "Operator is up."
      exit 0
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
  echo
  echo "Timed out waiting for operator readiness."
  tail -n 40 "${log_path}"
  exit 1
fi
