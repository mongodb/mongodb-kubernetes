#!/usr/bin/env bash
#
# Deprecation shim for the legacy `dc_select_network.sh` flags. The real
# implementation moved into Python (`scripts/dev/wt_ctl/domains/network.py`)
# in Phase 2.5 of wt-ctl. This file now translates the legacy flag style
# to the equivalent `wt-ctl network <subcommand>` invocation and execs it.
#
# Legacy   ->  New
# ---------    ----------------------------------------------------------
# --list                            ->  wt-ctl network list
# --prune  [--dry-run] [--networks] ->  wt-ctl network prune [--dry-run] [--networks]
# --release <branch_dir>            ->  wt-ctl network release <branch_dir>
# --branch-dir <branch_dir>         ->  wt-ctl network allocate <branch_dir>
# (no args)                         ->  wt-ctl network allocate
#
# Subcommands also pass through unchanged: `list`, `prune`, `release …`,
# `allocate …` work directly. Useful for callers transitioning to the new
# CLI a piece at a time.

set -Eeou pipefail

echo "[deprecated] dc_select_network.sh — use 'wt-ctl network' instead" >&2

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
wt_ctl="${script_dir}/wt-ctl"

# Translate legacy flags to the new subcommand surface. We accept both
# styles in one pass: anything that already looks like a subcommand
# falls through unchanged.
translated=()
case "${1:-}" in
  --list)
    translated=(list); shift ;;
  --prune)
    translated=(prune); shift ;;
  --release)
    if [[ $# -lt 2 ]]; then
      echo "ERROR: --release requires a branch_dir argument" >&2
      exit 2
    fi
    translated=(release "$2"); shift 2 ;;
  --branch-dir)
    if [[ $# -lt 2 ]]; then
      echo "ERROR: --branch-dir requires a value" >&2
      exit 2
    fi
    translated=(allocate "$2"); shift 2 ;;
  -h|--help)
    translated=(--help); shift ;;
  list|prune|release|allocate|"")
    # Already in new form (or no args -> default `allocate`).
    if [[ -z "${1:-}" ]]; then
      translated=(allocate)
    else
      translated=("$1"); shift
    fi
    ;;
  *)
    echo "ERROR: Unknown arg: $1" >&2
    exit 2 ;;
esac

# Append remaining args (e.g. --dry-run, --networks).
exec "${wt_ctl}" --quiet network "${translated[@]}" "$@"
