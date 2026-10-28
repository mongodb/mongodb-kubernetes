#!/usr/bin/env bash
#
# Start the operator locally (go run ./main.go) in a tmux session named 'mck-operator', which outlives the
# `devcontainer exec` call that started it. The log is logs/operator-<timestamp>.log, and logs/operator.log
# links to the latest one.
#
#   (no args)  start, wait until the operator is up, then tail the log. Ctrl-C stops the operator.
#   --detach   start, wait until the operator is up, then return. The operator keeps running.
#   --stop     stop the operator and whatever holds its ports.
#

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

# The operator binds 8181 (health, metrics) and 9443 (webhook). A previous unclean exit leaves them taken.
stop_operator() {
  tmux kill-session -t mck-operator 2>/dev/null || true
  for port in 8181 9443; do
    pid="$(lsof -ti:"${port}" 2>/dev/null || true)"
    if [[ -n "${pid}" ]]; then
      echo "killing stale pid ${pid} on port ${port}"
      kill -9 "${pid}" 2>/dev/null || true
    fi
  done
}

mode="tail"
case "${1:-}" in
  "") ;;
  --detach) mode="detach" ;;
  --stop) stop_operator; exit 0 ;;
  -h|--help) sed -n '3,9p' "$0"; exit 0 ;;
  *) echo "Unknown argument: $1" >&2; exit 1 ;;
esac

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${script_dir}/../.."
# shellcheck disable=SC1091
. scripts/dev/devenv

mkdir -p logs
log_path="logs/operator-$(date +%Y%m%d-%H%M%S).log"
ln -sf "${log_path#logs/}" logs/operator.log

stop_operator

# Passing any --watch-resource disables the default list, so a multi-cluster operator names all of them.
operator_args=""
proxy_env=""
if [[ "${KUBE_ENVIRONMENT_NAME:-}" == "multi" ]]; then
  operator_args="--watch-resource=mongodb --watch-resource=mongodbusers --watch-resource=opsmanagers \
--watch-resource=mongodbcommunity --watch-resource=mongodbsearch --watch-resource=clustermongodbroles \
--watch-resource=mongodbmulticluster"
  # The member clusters of a worktree on an Evergreen host are on the host's loopback, reached through the gost sidecar. The
  # operator's member healthcheck takes its proxy from this variable, not from HTTP_PROXY: Go never proxies 127.0.0.1.
  if [[ -n "${EVG_HOST_PROXY:-}" && -f .generated/.current-evg-host ]]; then
    proxy_env="MULTI_CLUSTER_HEALTHCHECK_PROXY=${EVG_HOST_PROXY}"
  fi
fi

tmux new-session -d -s mck-operator \
  "bash -lc 'cd \"$(pwd)\"; . scripts/dev/devenv; ${proxy_env} go run ./main.go ${operator_args} 2>&1 | tee ${log_path}'"
echo "Operator started in tmux session 'mck-operator', log ${log_path} (tmux a -t mck-operator to attach)."

ready=0
echo -n "Waiting for the operator "
for _ in $(seq 1 90); do
  if grep -qE 'Starting workers|webhook .* listening|Starting EventSource|controller=.* started' "${log_path}" 2>/dev/null; then
    ready=1
    break
  fi
  if grep -qE 'panic:|fatal:|level=error' "${log_path}" 2>/dev/null; then
    break
  fi
  echo -n "."
  sleep 1
done
if [[ ${ready} -eq 0 ]]; then
  echo "failed or timed out:"
  tail -n 40 "${log_path}"
  exit 1
fi
echo "up."

if [[ "${mode}" == "detach" ]]; then
  exit 0
fi

# bash runs the trap once tail dies on SIGINT.
trap 'echo; echo "Stopping the operator."; tmux kill-session -t mck-operator 2>/dev/null || true; exit 0' INT TERM
echo "Tailing ${log_path}. Ctrl-C stops the operator."
tail -F "${log_path}"
