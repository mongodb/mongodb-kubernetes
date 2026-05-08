#!/usr/bin/env bash
#
# End-to-end worktree development orchestrator.
#
# Given a target branch, this script:
#   1. Creates (or re-uses) a sibling git worktree for that branch.
#   2. In parallel:
#        - Prepares an Evergreen host with kind clusters
#          (scripts/dev/evg_prepare.sh).
#        - Builds the devcontainer image (scripts/dev/dc_build.sh).
#   3. Boots the devcontainer (`devcontainer up`).
#   4. Runs `make prepare-local-e2e` inside the devcontainer.
#   5. Prints the attach command for the user / agents.
#
# Usage:
#   wt_setup.sh [options] <branch>
#
# Options:
#   --evg-host-name NAME    Override the EVG host display name (default: the
#                           worktree directory name).
#   --context CTX           Switch the worktree to context CTX before running
#                           the pipeline. Useful when the new worktree should
#                           start on a different variant than the source repo.
#   --multi-cluster         Use the 4-cluster multi setup (e2e-operator,
#                           e2e-cluster-{1,2,3}, kind). Default is single.
#   --skip-recreate         Skip the kind cluster recreate step on the EVG
#                           host (it's the slow part). Use to quickly
#                           re-prepare against an already-running cluster.
#   --skip-evg              Skip evg-host preparation entirely (assume host
#                           is already set up; just run devcontainer + e2e).
#   --skip-devcontainer     Skip devcontainer build + up (only set up the
#                           worktree and the EVG host).
#   --skip-prepare-e2e      Don't run prepare-local-e2e after boot.
#   --force                 Pass -f to create_worktree.sh (re-init).
#   --log-dir DIR           Where to write parallel-stage logs (default:
#                           <worktree>/logs/setup_worktree).
#
# Exit non-zero if any phase fails. Logs for the parallel phases are written
# to the log dir even on success, so they're available for review.
#

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/funcs/printing

usage() { sed -n '3,33p' "$0"; }

evg_host_name=""
context=""
multi_cluster=0
skip_recreate=0
skip_evg=0
skip_devcontainer=0
skip_prepare_e2e=0
force=0
log_dir=""

positional=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --evg-host-name)     evg_host_name="$2"; shift 2 ;;
    --context)           context="$2"; shift 2 ;;
    --multi-cluster)     multi_cluster=1; shift ;;
    --skip-recreate)     skip_recreate=1; shift ;;
    --skip-evg)          skip_evg=1; shift ;;
    --skip-devcontainer) skip_devcontainer=1; shift ;;
    --skip-prepare-e2e)  skip_prepare_e2e=1; shift ;;
    --force)             force=1; shift ;;
    --log-dir)           log_dir="$2"; shift 2 ;;
    -h|--help)           usage; exit 0 ;;
    --)                  shift; positional+=("$@"); break ;;
    -*) echo "Unknown option: $1" >&2; usage; exit 1 ;;
    *) positional+=("$1"); shift ;;
  esac
done

if [[ ${#positional[@]} -ne 1 ]]; then
  echo "ERROR: exactly one branch name is required." >&2
  usage; exit 1
fi
branch="${positional[0]}"
branch_dir="${branch//\//_}"

# Locate the source repo. We may be invoked from the main clone or from any
# linked worktree of it. create_worktree.sh expects ${PROJECT_DIR} to be the
# directory under which sibling worktrees go; that's the dir containing this
# repo. Use the *current* worktree as PROJECT_DIR so a sibling is created
# next to it (../<branch_dir>).
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
src_repo_root="$(cd "${script_dir}/../.." && pwd)"
export PROJECT_DIR="${src_repo_root}"

target_worktree="${src_repo_root}/../${branch_dir}"

# Phase 1 — worktree.
if [[ "$(realpath "${src_repo_root}")" == "$(realpath "${target_worktree}" 2>/dev/null || echo /__nope__)" ]]; then
  echo "==> Already inside worktree for branch '${branch}'; skipping create_worktree.sh"
  worktree_root="${src_repo_root}"
else
  echo "==> Creating / initializing worktree for branch '${branch}'"
  create_args=()
  [[ ${force} -eq 1 ]] && create_args+=(-f)
  bash "${src_repo_root}/scripts/dev/create_worktree.sh" "${create_args[@]}" "${branch}"
  worktree_root="$(realpath "${target_worktree}")"
fi
echo "==> worktree_root = ${worktree_root}"

cd "${worktree_root}"

# Default the log directory under the target worktree.
[[ -z "${log_dir}" ]] && log_dir="${worktree_root}/logs/setup_worktree"
mkdir -p "${log_dir}"

# If a context override was passed, switch the worktree to it now so the
# rest of the pipeline (evg_prepare's `make switch`, recreate_kind*,
# prepare-local-e2e, the in-container `make switch`) all see the same
# context. Without this the new worktree inherits whatever context the
# source repo had pinned in .generated/.current_context.
if [[ -n "${context}" ]]; then
  echo "==> Switching worktree to context '${context}'"
  make switch context="${context}"
fi

# Resolve the EVG host name we'll use: override > worktree dir basename.
[[ -z "${evg_host_name}" ]] && evg_host_name="$(basename "${worktree_root}")"
echo "==> evg_host_name = ${evg_host_name}"

# Run the devcontainer initializeCommand on the host before forking.
# `devcontainer build` does NOT run initializeCommand, so without this the
# compose.generated.yml / compose.user.yml that the build references would be
# missing on first run. `devcontainer up` runs it on its own; we re-run it
# here so the build has the same view.
if [[ ${skip_devcontainer} -eq 0 && -f .devcontainer/scripts/initialize.sh ]]; then
  echo "==> Running .devcontainer/scripts/initialize.sh"
  bash .devcontainer/scripts/initialize.sh
fi

# Pick a free 172.X.0.0/16 subnet for this worktree's compose stack and
# persist it in .devcontainer/.env so 'devcontainer build' and 'devcontainer
# up' (and any later 'devcontainer exec') agree on the same prefix. Multiple
# worktrees coexist as long as each has a distinct prefix.
if [[ ${skip_devcontainer} -eq 0 ]]; then
  net_env_file=".devcontainer/.env"
  if [[ -f "${net_env_file}" ]] && grep -q '^MCK_DEVC_NET_PREFIX=' "${net_env_file}"; then
    # Reuse the previously-chosen prefix for this worktree.
    chosen_line="$(grep '^MCK_DEVC_NET_PREFIX=' "${net_env_file}" | tail -n1)"
  else
    chosen_line="$(bash scripts/dev/dc_select_network.sh --branch-dir "${branch_dir}")"
    echo "${chosen_line}" >> "${net_env_file}"
  fi
  export "${chosen_line?}"
  echo "==> Devcontainer network: ${chosen_line} (subnet 172.${chosen_line#MCK_DEVC_NET_PREFIX=}.0.0/16)"
fi

# Phase 2 — parallel evg + devcontainer build.
evg_pid=""
build_pid=""
evg_log="${log_dir}/evg_prepare.log"
build_log="${log_dir}/dc_build.log"

if [[ ${skip_evg} -eq 0 ]]; then
  evg_args=(--name "${evg_host_name}")
  [[ ${multi_cluster} -eq 1 ]] && evg_args+=(--multi)
  [[ ${skip_recreate} -eq 1 ]] && evg_args+=(--skip-recreate)
  echo "==> Launching evg_prepare.sh in background (full log: ${evg_log})"
  # Full output -> ${evg_log}; high-signal filtered, prepended with 'evg-host'
  # -> stdout for live interleaved view alongside the build phase.
  ( bash scripts/dev/evg_prepare.sh "${evg_args[@]}" 2>&1 \
      | tee "${evg_log}" \
      | filter_buildx_noise \
      | prepend "evg-host" ) &
  evg_pid=$!
fi

if [[ ${skip_devcontainer} -eq 0 ]]; then
  echo "==> Launching dc_build.sh in background (full log: ${build_log})"
  ( bash scripts/dev/dc_build.sh --workspace-folder "${worktree_root}" 2>&1 \
      | tee "${build_log}" \
      | filter_buildx_noise \
      | prepend "build" ) &
  build_pid=$!
fi

failures=()
if [[ -n "${evg_pid}" ]]; then
  if ! wait "${evg_pid}"; then
    failures+=("evg_prepare (see ${evg_log})")
  fi
fi
if [[ -n "${build_pid}" ]]; then
  if ! wait "${build_pid}"; then
    failures+=("dc_build (see ${build_log})")
  fi
fi
if [[ ${#failures[@]} -gt 0 ]]; then
  echo
  echo "ERROR: parallel phase failures:"
  printf '  - %s\n' "${failures[@]}"
  echo
  echo "==> Tail of evg log:";  tail -n 60 "${evg_log}"   2>/dev/null || true
  echo "==> Tail of build log:"; tail -n 60 "${build_log}" 2>/dev/null || true
  exit 1
fi

# Phase 3 — bring the devcontainer up + run prepare-local-e2e.
if [[ ${skip_devcontainer} -eq 0 ]]; then
  up_log="${log_dir}/dc_up.log"
  echo "==> Booting devcontainer (full log: ${up_log})"
  bash scripts/dev/dc_up.sh --workspace-folder "${worktree_root}" 2>&1 \
    | tee "${up_log}" \
    | filter_buildx_noise \
    | prepend "up"

  # Ensure the ssh-agent fan-out is alive. post-start.sh creates a screen
  # session that bridges /ssh-auth.sock (host-mounted) to /ssh-agent/socket
  # (which the autossh sidecar reads). If the screen died on a prior boot,
  # postStartCommand isn't always re-run on `devcontainer up`, leaving
  # autossh stuck and gost-proxy returning Service Unavailable. Re-running
  # post-start.sh is idempotent (it wipes dead screens first).
  echo "==> Ensuring ssh-agent fan-out is alive in devcontainer"
  devcontainer exec --workspace-folder "${worktree_root}" bash -lc '
    if [[ ! -S /ssh-agent/socket ]]; then
      echo "ssh-agent socket missing; re-running post-start.sh"
      bash /workspace/.devcontainer/scripts/post-start.sh
    else
      echo "ssh-agent socket already present"
    fi'

  # Inside the container we need to (a) regenerate context*.env so
  # PROJECT_DIR resolves to /workspace (the host-generated copy bakes in
  # the Mac path), and (b) patch the kubeconfig with proxy-url + register
  # with the k8s-forwarding-proxy. The kubeconfig itself is bind-mounted
  # from the host (no second scp needed) — and the main devcontainer
  # service has no known_hosts mount, so scp would fail anyway.
  refresh_log="${log_dir}/refresh_kubeconfig.log"
  echo "==> Regenerating context + patching kubeconfig inside devcontainer (full log: ${refresh_log})"
  # shellcheck disable=SC2016  # $(...) is expanded inside the bash -lc subshell, not here.
  devcontainer exec --workspace-folder "${worktree_root}" \
    bash -lc 'set -Eeou pipefail
              cd /workspace
              make switch context="$(cat .generated/.current_context)"
              bash scripts/dev/evg_host.sh get-kubeconfig --no-fetch' \
    2>&1 | tee "${refresh_log}" | filter_buildx_noise | prepend "refresh"

  if [[ ${skip_prepare_e2e} -eq 0 ]]; then
    e2e_log="${log_dir}/prepare_local_e2e.log"
    echo "==> Running 'make prepare-local-e2e' inside the devcontainer (full log: ${e2e_log})"
    devcontainer exec --workspace-folder "${worktree_root}" \
      bash -lc 'set -Eeou pipefail; cd /workspace && make prepare-local-e2e' \
      2>&1 | tee "${e2e_log}" | filter_buildx_noise | prepend "prepare-e2e"
  fi
fi

echo
echo "==> Worktree dev setup complete."
echo "    worktree:        ${worktree_root}"
echo "    evg host name:   ${evg_host_name}"
echo "    logs dir:        ${log_dir}"
if [[ ${skip_devcontainer} -eq 0 ]]; then
  echo
  echo "Attach the dev tmux workspace (k9s + operator log + e2e log + zsh):"
  echo "  ${worktree_root}/scripts/dev/dc_attach.sh"
  echo
  echo "Run a bare shell or one-off command inside the container instead:"
  echo "  ${worktree_root}/scripts/dev/dc_attach.sh bash      # or zsh, make foo, ..."
fi
