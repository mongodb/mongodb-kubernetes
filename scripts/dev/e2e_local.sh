#!/usr/bin/env bash
#
# One-shot local e2e: create a fresh isolated stack (worktree + EVG host +
# devcontainer + kind cluster[s]) and run one pytest marker against it.
#
# Usage:
#   e2e_local.sh --marker <marker> --context <context> [options]
#
# Options:
#   --branch <name>        branch/worktree to create (default: e2e-local-<utc ts>)
#   --single-cluster       single kind cluster instead of the multi-cluster default
#   --skip-create          reuse the existing stack for --branch
#   --teardown             `wt-ctl delete --all` after the run
#   --create-timeout <s>   max seconds to wait for create (default: 3600)
#   --test-timeout <s>     max seconds to wait for the test (default: 7200)
#   -h | --help            this help
#
# Run it from the checkout that should parent the new worktree (e.g. a master
# worktree): the new branch is cut from that checkout's HEAD.
#
# Prints `PHASE <name> <UTC ts>` lines (start, create_started, create_done,
# test_start, test_end, teardown_*, done), a `RESULT ...` line, writes
# <worktree>/logs/oneshot-summary.txt, and exits with the test's exit code.
#

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tooling="$(cd "${script_dir}/../.." && pwd)"
wt_ctl="${tooling}/scripts/dev/wt-ctl"
create_logs="${HOME}/.cache/mck-devc/create-logs"

branch=""
context=""
marker=""
single=0
skip_create=0
teardown=0
create_timeout=3600
test_timeout=7200

usage() { sed -n '3,27p' "$0"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --branch) branch="$2"; shift 2 ;;
    --context) context="$2"; shift 2 ;;
    --marker) marker="$2"; shift 2 ;;
    --single-cluster) single=1; shift ;;
    --skip-create) skip_create=1; shift ;;
    --teardown) teardown=1; shift ;;
    --create-timeout) create_timeout="$2"; shift 2 ;;
    --test-timeout) test_timeout="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "e2e_local.sh: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ -n "${marker}" ]] || { echo "e2e_local.sh: --marker is required" >&2; exit 2; }
[[ -n "${context}" ]] || { echo "e2e_local.sh: --context is required" >&2; exit 2; }
if [[ -z "${branch}" ]]; then
  branch="e2e-local-$(date -u +%Y%m%d-%H%M%S)"
fi
branch_dir="${branch//\//_}"
invocation_dir="${PWD}"
worktree_path="$(dirname "${invocation_dir}")/${branch_dir}"
topology="multi"
[[ ${single} -eq 1 ]] && topology="single"

phase() { echo "PHASE $1 $(date -u +%Y-%m-%dT%H:%M:%SZ)"; }

phase start
echo "e2e_local.sh: branch=${branch} context=${context} marker=${marker} topology=${topology}"
echo "e2e_local.sh: worktree=${worktree_path}"

create_rc=0
if [[ ${skip_create} -eq 0 ]]; then
  phase create_started
  create_args=(create --detach --context "${context}")
  [[ ${single} -eq 1 ]] && create_args+=(--single-cluster)
  create_args+=("${branch}")
  "${wt_ctl}" "${create_args[@]}"
  set +e
  "${wt_ctl}" wait "${branch}" --timeout "${create_timeout}"
  create_rc=$?
  set -e
  if [[ ${create_rc} -ne 0 ]]; then
    echo "e2e_local.sh: create FAILED (exit ${create_rc}); log tail:" >&2
    tail -n 25 "${create_logs}/${branch_dir}.log" >&2 2>/dev/null || true
    phase create_failed
    exit "${create_rc}"
  fi
  phase create_done
else
  echo "e2e_local.sh: --skip-create; reusing the existing stack for ${branch}"
fi

[[ -d "${worktree_path}" ]] || { echo "e2e_local.sh: worktree not found: ${worktree_path}" >&2; exit 1; }
cd "${worktree_path}"

phase test_start
attach_rc=0
"${wt_ctl}" attach -- bash /mck-tooling/scripts/dev/e2e_run.sh --detach "${marker}" >/dev/null || attach_rc=$?
if [[ ${attach_rc} -ne 0 ]]; then
  echo "e2e_local.sh: failed to launch the test (attach exit ${attach_rc})" >&2
  exit "${attach_rc}"
fi

test_rc=0
set +e
"${wt_ctl}" wait --test --timeout "${test_timeout}"
test_rc=$?
set -e
phase test_end

summary_line="$(grep -Eo '[0-9]+ (passed|failed).*' logs/test.log 2>/dev/null | tail -n1 || true)"
echo "RESULT test_rc=${test_rc} marker=${marker} branch=${branch}${summary_line:+ summary=\"${summary_line}\"}"

{
  echo "branch=${branch}"
  echo "context=${context}"
  echo "marker=${marker}"
  echo "topology=${topology}"
  echo "worktree=${worktree_path}"
  echo "test_rc=${test_rc}"
  echo "summary=${summary_line}"
} > logs/oneshot-summary.txt

if [[ ${teardown} -eq 1 ]]; then
  phase teardown_started
  if ! "${wt_ctl}" delete --all "${branch}"; then
    echo "e2e_local.sh: teardown failed; the stack may still be running" >&2
  fi
  phase teardown_done
else
  echo "e2e_local.sh: stack left running — tear down with: (cd ${worktree_path} && ${wt_ctl} delete --all ${branch})"
fi

phase "done"
exit "${test_rc}"
