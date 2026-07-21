#!/usr/bin/env bash
#
# Sourceable helper for scripts that want the per-side env loaded
# without re-implementing the bootstrap. Prefer sourcing
# scripts/dev/devenv directly; this is a thin wrapper for the many
# scripts that source this helper to load the generated context env.
#
# Behavior:
#   - Resolves the worktree root via the script's own location.
#   - Sources scripts/dev/devenv (which fails loudly if env files
#     are missing, picks the right side via /.dockerenv, sources
#     logical .generated/context.env + site .generated/context.<side>.env
#     with `set -a`, and activates venv if present).
#   - Does NOT prepend ${PROJECT_DIR}/bin to PATH — that's handled
#     in the container by /etc/profile.d/mck-bin.sh, and on the host
#     by the dev's own ~/.zshrc / ~/.bashrc.

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

_set_env_context_script="$(readlink -f "${BASH_SOURCE[0]}")"
_set_env_context_dir="$(dirname "${_set_env_context_script}")"
_set_env_context_root="$(realpath "${_set_env_context_dir}/../..")"

# shellcheck disable=SC1091
. "${_set_env_context_root}/scripts/dev/devenv"

# Prepend the repo-local bin/ (downloaded kubectl/helm, bin/reset, chart-testing
# tools) to PATH for the ~40 scripts that source this helper. Interactive shells
# also get this via the user's rc (host) or
# /etc/profile.d/mck-bin.sh (container), but non-interactive script invocations
# don't — so restore it here. Idempotent: skip if already present.
if [[ -n "${PROJECT_DIR:-}" ]]; then
  case ":${PATH}:" in
    *":${PROJECT_DIR}/bin:"*) ;;
    *) export PATH="${PROJECT_DIR}/bin:${PATH}" ;;
  esac
fi

unset _set_env_context_script _set_env_context_dir _set_env_context_root
