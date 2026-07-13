#!/usr/bin/env bash
# Bring up N devc worktrees in parallel, each on its own fresh EVG host (own
# Docker daemon -> stock kind cluster names never collide). Output per env is
# prefixed [<tag>] and tee'd to logs/e2e_local_parallel_devcontainers/<tag>.log.
# Unless --keep, everything is torn down at the end.
set -Eeuo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WT_CTL="${REPO}/scripts/dev/wt-ctl"
LOG_DIR="${REPO}/logs/e2e_local_parallel_devcontainers"

# tag  context  marker  [extra wt-ctl create flags...]
# marker is the pytest marker run (via e2e_run.sh) once the env is up; the
# triples mirror the e2e_devc_* tasks in .evergreen-tasks.yml.
ENVS=(
  "search_mc_rs e2e_multi_cluster_kind   e2e_search_connectivity_tool_mc_rs"
  "search_rs    e2e_mdb_kind_ubi_cloudqa e2e_search_connectivity_tool       --single-cluster"
  "om_pod_spec  e2e_om80_kind_ubi        e2e_om_ops_manager_pod_spec        --single-cluster"
)

KEEP=0
for a in "$@"; do [[ "${a}" == "--keep" ]] && KEEP=1; done

branch_of() { echo "devc-e2e/$1"; }
worktree_of() { echo "${REPO%/*}/devc-e2e_$1"; }

mkdir -p "${LOG_DIR}"

# Teardown leftovers up front so each env starts clean. Deleting the git
# branch (not just the worktree) is essential: create_worktree.sh checks out
# an EXISTING branch verbatim, so a stale devc-e2e/<tag> ref would run old
# code. Removing it forces recreation from the current HEAD every run.
for entry in "${ENVS[@]}"; do
  read -ra p <<<"${entry}"
  br="$(branch_of "${p[0]}")"
  "${WT_CTL}" --color never delete "${br}" --all || true
  git -C "${REPO}" worktree prune || true
  git -C "${REPO}" branch -D "${br}" 2>/dev/null || true
done

# Fresh create per env, then run the marker inside its devc; in parallel.
# create failure short-circuits the test (&&). Live output is prefixed
# [<tag>] and tee'd to a per-env log; pipefail makes the pipeline exit
# reflect create/test, not the tee.
run_one() {
  local tag=$1 context=$2 marker=$3; shift 3
  set -o pipefail
  # Per-env TMPDIR: the devcontainer CLI stages features + compose fragments
  # under $TMPDIR/devcontainercli/, and concurrent `devcontainer up` runs on one
  # host clobber that shared tree (ENOENT on aws-cli_0/devcontainer-feature.json,
  # or `docker compose up` failing on a vanished -f fragment). Isolate it per env.
  local envtmp; envtmp="$(mktemp -d "${TMPDIR:-/tmp}/devc-par-${tag}.XXXXXX")"
  export TMPDIR="${envtmp}"
  local rc=0
  {
    "${WT_CTL}" --color never create "$(branch_of "${tag}")" --context "${context}" "$@" --force \
      && ( cd "$(worktree_of "${tag}")" \
             && "${WT_CTL}" --color never attach -- scripts/dev/e2e_run.sh "${marker}" )
  } 2>&1 \
    | tee "${LOG_DIR}/${tag}.log" \
    | sed -u "s/^/[${tag}] /" || rc=$?
  rm -rf "${envtmp}" || true
  return "${rc}"
}

pids=(); tags=()
for entry in "${ENVS[@]}"; do
  read -ra p <<<"${entry}"
  run_one "${p[0]}" "${p[1]}" "${p[2]}" "${p[@]:3}" &
  pids+=("$!"); tags+=("${p[0]}")
done

rc=0
for i in "${!pids[@]}"; do
  if wait "${pids[i]}"; then echo "[${tags[i]}] OK"; else echo "[${tags[i]}] FAILED"; rc=1; fi
done
echo "logs under ${LOG_DIR}"

if [[ "${KEEP}" == 1 ]]; then
  echo "--keep: leaving worktrees + EVG hosts up."
else
  echo "tearing down..."
  for entry in "${ENVS[@]}"; do
    read -ra p <<<"${entry}"
    "${WT_CTL}" --color never delete "$(branch_of "${p[0]}")" --all || true
  done
fi

exit "${rc}"
