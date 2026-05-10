#!/usr/bin/env bash
#
# DEPRECATED — use ``scripts/dev/wt-ctl delete`` instead.
#
# This shim translates ``wt_teardown.sh`` flags to the new verb. The
# ``--keep-evg-host`` form is renamed to ``--keep-evg`` (matching wt-ctl's
# concise primitives). All other flags map 1:1.
#
# Usage:
#   wt_teardown.sh [options] <branch>
#
# Options (forwarded):
#   --delete-branch    → wt-ctl delete --delete-branch
#   --keep-evg-host    → wt-ctl delete --keep-evg
#   --keep-stack       → wt-ctl delete --keep-stack
#   --keep-worktree    → wt-ctl delete --keep-worktree
#   --keep-om-projects → wt-ctl delete --keep-om-projects
#   --evg-host-name N  → wt-ctl delete --evg-host-name N
#
set -Eeuo pipefail

usage() { sed -n '3,22p' "$0"; }

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "[deprecated] wt_teardown.sh: use 'wt-ctl delete' (this shim now forwards)." >&2

forwarded=()
positional=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --delete-branch)    forwarded+=(--delete-branch); shift ;;
    --keep-evg-host)    forwarded+=(--keep-evg); shift ;;
    --keep-evg)         forwarded+=(--keep-evg); shift ;;
    --keep-stack)       forwarded+=(--keep-stack); shift ;;
    --keep-worktree)    forwarded+=(--keep-worktree); shift ;;
    --keep-om-projects) forwarded+=(--keep-om-projects); shift ;;
    --evg-host-name)    forwarded+=(--evg-host-name "$2"); shift 2 ;;
    -h|--help)          usage; exit 0 ;;
    -*) echo "[wt_teardown.sh] unknown option: $1" >&2; usage; exit 1 ;;
    *)  positional+=("$1"); shift ;;
  esac
done

if [[ ${#positional[@]} -ne 1 ]]; then
  echo "[wt_teardown.sh] exactly one branch name is required." >&2
  usage; exit 1
fi

exec "${script_dir}/wt-ctl" delete "${forwarded[@]}" "${positional[0]}"
