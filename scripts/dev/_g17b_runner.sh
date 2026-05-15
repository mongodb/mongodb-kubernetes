#!/usr/bin/env bash
# G iter 17b takeover runner — disposable. Runs e2e_run.sh inside devc,
# writes the log to /workspace/logs/e2e-G17b-takeover.log, drops a
# sentinel file /workspace/logs/e2e-G17b-takeover.done containing the
# pytest exit code on completion. Designed for foreground-chain polling
# from a subagent that can't use run_in_background.

set -uo pipefail

LOG=/workspace/logs/e2e-G17b-takeover.log
DONE=/workspace/logs/e2e-G17b-takeover.done

rm -f "${DONE}"
{
  /workspace/scripts/dev/e2e_run.sh docker/mongodb-kubernetes-tests/tests/multicluster_shardedcluster/multi_cluster_sharded_takeover.py
  ec=$?
  echo "${ec}" > "${DONE}"
} > "${LOG}" 2>&1 &
echo $! > /workspace/logs/e2e-G17b-takeover.pid
disown
echo "Launched pid=$(cat /workspace/logs/e2e-G17b-takeover.pid), log=${LOG}, done=${DONE}"
