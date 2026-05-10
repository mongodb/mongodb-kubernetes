#!/usr/bin/env bash
#
# DEPRECATED — use ``scripts/dev/wt-ctl create`` instead.
#
# This shim translates the old ``wt_setup.sh`` flag set into ``wt-ctl
# create``. Most flags map 1:1; ``--evg-host-name``, ``--multi-cluster``,
# ``--skip-recreate``, ``--skip-evg``, ``--skip-devcontainer``,
# ``--skip-prepare-e2e``, ``--force``, ``--context`` all forward verbatim.
# ``--log-dir`` is dropped (wt-ctl uses ``<worktree>/logs/setup_worktree/``
# unconditionally — that was already the default in the bash version).
#
# Usage:
#   wt_setup.sh [options] <branch>
#
# Options (forwarded):
#   --evg-host-name NAME    → wt-ctl create --evg-host-name NAME
#   --context CTX           → wt-ctl create --context CTX
#   --multi-cluster         → wt-ctl create --multi-cluster
#   --skip-recreate         → wt-ctl create --skip-recreate
#   --skip-evg              → wt-ctl create --skip-evg
#   --skip-devcontainer     → wt-ctl create --skip-devcontainer
#   --skip-prepare-e2e      → wt-ctl create --skip-prepare-e2e
#   --force                 → wt-ctl create --force
#   --log-dir DIR           Dropped (no replacement; logs go to
#                           <worktree>/logs/setup_worktree/).
#
set -Eeuo pipefail

usage() { sed -n '3,30p' "$0"; }

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "[deprecated] wt_setup.sh: use 'wt-ctl create' (this shim now forwards)." >&2

forwarded=()
positional=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --evg-host-name)     forwarded+=(--evg-host-name "$2"); shift 2 ;;
    --context)           forwarded+=(--context "$2"); shift 2 ;;
    --multi-cluster)     forwarded+=(--multi-cluster); shift ;;
    --skip-recreate)     forwarded+=(--skip-recreate); shift ;;
    --skip-evg)          forwarded+=(--skip-evg); shift ;;
    --skip-devcontainer) forwarded+=(--skip-devcontainer); shift ;;
    --skip-prepare-e2e)  forwarded+=(--skip-prepare-e2e); shift ;;
    --force)             forwarded+=(--force); shift ;;
    --log-dir)           shift 2 ;;  # dropped
    -h|--help)           usage; exit 0 ;;
    --) shift; positional+=("$@"); break ;;
    -*) echo "[wt_setup.sh] unknown option: $1" >&2; usage; exit 1 ;;
    *)  positional+=("$1"); shift ;;
  esac
done

if [[ ${#positional[@]} -ne 1 ]]; then
  echo "[wt_setup.sh] exactly one branch name is required." >&2
  usage; exit 1
fi

exec "${script_dir}/wt-ctl" create "${forwarded[@]}" "${positional[0]}"
