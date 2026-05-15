#!/usr/bin/env bash
# G iter 17b prepare-local-e2e runner — wraps prepare_local_e2e_run.sh
# with a sentinel-based completion file so a foreground-chain poller
# can wait without using run_in_background.

set -uo pipefail

LOG=/workspace/logs/g17b-prepare.log
DONE=/workspace/logs/g17b-prepare.done

rm -f "${DONE}"
{
  cd /workspace
  RESET=true /workspace/scripts/dev/prepare_local_e2e_run.sh
  ec=$?
  echo "${ec}" > "${DONE}"
} > "${LOG}" 2>&1 &
echo $! > /workspace/logs/g17b-prepare.pid
disown
echo "Launched pid=$(cat /workspace/logs/g17b-prepare.pid), log=${LOG}, done=${DONE}"
