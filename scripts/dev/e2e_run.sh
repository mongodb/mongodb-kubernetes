#!/usr/bin/env bash
#
# Run one e2e test against the worktree's clusters, with the operator already running (op_run.sh).
#
# Usage: e2e_run.sh <marker | tests/path/to/test.py>
#
# The log is logs/test-<target>-<timestamp>.log (logs/test.log links to the latest), the JUnit report is
# logs/myreport.xml, and the exit code is pytest's.
#

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

if [[ $# -ne 1 || "$1" == -* ]]; then
  sed -n '3,10p' "$0"
  exit 1
fi
target="$1"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "${script_dir}/../.." && pwd)"
cd "${root}"
# shellcheck disable=SC1091
source scripts/dev/set_env_context.sh

# An expired registry token fails the first image pull minutes into the test, so refuse to start with one. The
# fix needs the AWS keys, which only the host has. A checkout without a store (not made by wt-ctl) and an
# Evergreen task host are not checked.
if [[ "${RUNNING_IN_EVG:-}" != "true" && -f .generated/registry-auth/config.json ]]; then
  set +e
  auth_report="$(scripts/dev/wt-ctl --quiet auth status 2>&1)"
  auth_rc=$?
  set -e
  case "${auth_rc}" in
    1) echo "WARNING: the registry credentials are expiring; on the host run: scripts/dev/wt-ctl auth refresh" >&2 ;;
    3)
      printf '%s\n' "${auth_report}" >&2
      echo "ERROR: the registry credentials are expired, so the first image pull would fail." >&2
      echo "       On the host (not in the container) run: scripts/dev/wt-ctl auth refresh" >&2
      exit 1
      ;;
  esac
fi

mkdir -p logs
log_path="logs/test-${target//[^A-Za-z0-9._-]/_}-$(date +%Y%m%d-%H%M%S).log"
ln -sf "${log_path#logs/}" logs/test.log

pytest_args=(-v -s "--junitxml=${root}/logs/myreport.xml")
if [[ "${target}" == */* || "${target}" == *.py ]]; then
  # pytest runs in docker/mongodb-kubernetes-tests: take a path from the repo root as it is, and anything else
  # (tests/<dir>/<file>.py) relative to that directory.
  if [[ -e "${root}/${target}" ]]; then
    pytest_args+=("${root}/${target}")
  else
    pytest_args+=("${target}")
  fi
else
  pytest_args+=(-m "${target}")
fi

cd docker/mongodb-kubernetes-tests
echo "Running: pytest ${pytest_args[*]}"
echo "Log: ${root}/${log_path}"
# tee would hide pytest's exit code.
set +e
pytest "${pytest_args[@]}" 2>&1 | tee "${root}/${log_path}"
exit "${PIPESTATUS[0]}"
