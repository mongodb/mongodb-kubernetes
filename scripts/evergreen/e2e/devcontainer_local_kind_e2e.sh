#!/usr/bin/env bash

# On-host e2e: drive the wt-ctl --local-kind flow directly on the EVG task
# host. kind runs on the task host, the operator runs as `go run ./main.go`
# and the tests run as pytest — both inside the devcontainer. No operator /
# init / database images are built: everything resolves to the :latest ECR
# tags (OVERRIDE_VERSION_ID is intentionally unset), so this variant carries
# no build_*_image dependencies.

set -Eeou pipefail

source scripts/funcs/printing

# wt-ctl streams per-phase output to logs/<phase>.log; parallel phases
# (evg_prepare + dc_build) don't all echo to stdout, and these logs aren't
# uploaded as task artifacts. On any failure, surface the tails so the EVG
# task log alone is enough to diagnose.
# shellcheck disable=SC2317  # body runs via the EXIT trap, not inline.
dump_logs_on_error() {
  local rc=$?
  [[ ${rc} -eq 0 ]] && return 0
  header "FAILURE (rc=${rc}) - dumping wt-ctl phase logs"
  while IFS= read -r f; do
    echo "===== tail -n 120 ${f} ====="
    tail -n 120 "${f}"
  done < <(find . -path ./.git -prune -o -type f -path '*/logs/*.log' -print 2>/dev/null | sort)
  return "${rc}"
}
trap dump_logs_on_error EXIT

# Marker to run: task-level e2e_marker expansion, else the task name (the
# native e2e.sh convention where TASK_NAME == pytest marker).
MARKER="${e2e_marker:-${TASK_NAME:?e2e_marker or TASK_NAME required}}"
CONTEXT="${e2e_context:-e2e_multi_cluster_kind}"
# Worktree/branch name doubles as the single-cluster kind cluster name, which
# must match ^[a-z0-9.-]+$ — markers carry underscores, so map them to dashes.
WT_NAME="${e2e_wt_name:-onhost-${MARKER//_/-}}"

# Go ships under /opt on EVG distros.
for d in /opt/golang/go*/bin; do [[ -d "${d}" ]] && PATH="${d}:${PATH}"; done
export PATH
command -v go >/dev/null || { echo "go not on PATH (expected under /opt/golang)"; exit 1; }

# The devcontainer CLI needs Node >=18. EVG distros vary wildly (2204 ships
# v24, 2404 ships v8) and /opt is root-owned, so we pick the newest usable
# /opt Node and otherwise install a modern one under $HOME. The CLI itself
# goes to a writable npm prefix to dodge EACCES on /opt.
node_bin="" node_major=0
for b in /opt/nodejs/node-v*/bin; do
  [[ -x "${b}/node" ]] || continue
  v="$("${b}/node" -e 'process.stdout.write(process.versions.node.split(".")[0])' 2>/dev/null)" || continue
  if [[ "${v}" =~ ^[0-9]+$ ]] && (( v > node_major )); then node_major="${v}"; node_bin="${b}"; fi
done
if [[ -n "${node_bin}" && "${node_major}" -ge 18 ]]; then
  PATH="${node_bin}:${PATH}"
else
  header "installing local Node (task-host Node too old: v${node_major})"
  node_ver="v22.14.0"; case "$(uname -m)" in x86_64) na="x64";; aarch64|arm64) na="arm64";; *) na="x64";; esac
  dest="${HOME}/.local/node"; mkdir -p "${dest}"
  curl -fsSL "https://nodejs.org/dist/${node_ver}/node-${node_ver}-linux-${na}.tar.xz" | tar -xJ -C "${dest}" --strip-components=1
  PATH="${dest}/bin:${PATH}"
fi
export PATH
if ! command -v devcontainer >/dev/null; then
  npm config set prefix "${HOME}/.npm-global"
  npm i -g @devcontainers/cli
  PATH="${HOME}/.npm-global/bin:${PATH}"; export PATH
fi

# This variant builds no images. By default use staging :latest (as the
# validated dev-host run did) rather than the per-patch tags the patch build
# scenario would force. But if OVERRIDE_VERSION_ID pins a specific build,
# honor it — root-context maps it onto every image tag — and don't force
# latest/staging over it.
if [[ -z "${OVERRIDE_VERSION_ID:-}" ]]; then
  export BUILD_SCENARIO=staging
  export OPERATOR_VERSION=latest READINESS_PROBE_VERSION=latest VERSION_UPGRADE_HOOK_VERSION=latest
  export DATABASE_VERSION=latest INIT_DATABASE_VERSION=latest INIT_OPS_MANAGER_VERSION=latest
fi

# A bare EVG task host has no kind/kubectl/helm; setup_evg_host.sh installs
# them (and raises inotify/file limits kind needs). Idempotent to re-run.
command -v kind >/dev/null || { header "setup_evg_host.sh (kind/kubectl/helm)"; scripts/dev/setup_evg_host.sh; }

# Topology and cloud_qa vs OM-in-cluster are properties of the context, not
# separate task vars. Switch to it and source the generated env so the context's
# variables are available here directly: a multi-cluster context exports
# KUBE_ENVIRONMENT_NAME=multi; a cloud_qa context exports
# ops_manager_version=cloud_qa (OM contexts export a concrete OM version and
# must NOT run setup_cloud_qa). The host has the EVG expansions private-context
# reads, so the switch resolves here; wt-ctl re-switches inside the container.
header "switching to context ${CONTEXT}"
make switch context="${CONTEXT}" >/dev/null
# shellcheck disable=SC1091
source scripts/dev/set_env_context.sh

TOPOLOGY_FLAG="--single-cluster"
[[ "${KUBE_ENVIRONMENT_NAME:-kind}" == "multi" ]] && TOPOLOGY_FLAG="--multi-cluster"
CLOUD_QA="false"
[[ "${ops_manager_version:-}" == "cloud_qa" ]] && CLOUD_QA="true"
echo "context ${CONTEXT}: KUBE_ENVIRONMENT_NAME=${KUBE_ENVIRONMENT_NAME:-kind} ops_manager_version=${ops_manager_version:-} -> ${TOPOLOGY_FLAG}, cloud_qa=${CLOUD_QA}"

# recreate_kind_clusters.sh (run by wt-ctl evg_prepare in local-kind mode)
# handles ECR login via configure_container_auth.sh, so image pulls of the
# :latest tags are already authenticated.

# `make switch` runs inside the devcontainer (devcontainer.json onCreate, and
# again in wt-ctl's kubeconfig phase), sourcing private-context (the copied
# evg-private-context) which reads these EVG expansions under `set -u`. The
# host shell has them but the container does not. private-context is
# bind-mounted (/workspace), so resolve the expansions to literal exports in
# the file itself — the container then sources concrete values, no env
# injection. Prepending keeps the existing `${var}` references satisfied and
# the host make switch (which already has the expansions) still works.
pc="scripts/dev/contexts/private-context"
if [[ -f "${pc}" ]]; then
  header "resolving EVG cred expansions into private-context"
  # The external inputs private-context reads with no `set -u` default. Only
  # host-agnostic values: the credential expansions plus EVR_TASK_ID. NOT
  # workdir — it's a host path (/data/mci/...) invalid inside the container,
  # which supplies its own valid workdir.
  { for v in EVR_TASK_ID \
             mms_eng_test_aws_access_key mms_eng_test_aws_secret mms_eng_test_aws_region \
             e2e_cloud_qa_apikey_owner_ubi_cloudqa e2e_cloud_qa_orgid_owner_ubi_cloudqa \
             e2e_cloud_qa_user_owner_ubi_cloudqa \
             cognito_user_name cognito_user_password cognito_user_pool_id \
             cognito_workload_federation_client_id cognito_workload_federation_client_secret \
             cognito_workload_url cognito_workload_user_id \
             community_private_preview_pullsecret_dockerconfigjson; do
      printf 'export %s=%q\n' "${v}" "${!v:-}"
    done
    cat "${pc}"
    # Container-only overrides for evg-private-context's CI-pod-oriented values
    # (host keeps its own — /.dockerenv is absent there):
    #  - GOROOT: the hardcoded /opt/golang/... is a host path, invalid in the
    #    container (which has its own go with a correct built-in GOROOT).
    #  - LOCAL_OPERATOR=true: this flow runs the operator locally via `go run`,
    #    so prepare-local-e2e must run the multi-cluster kube-config creator
    #    (which builds the operator member-list ConfigMap); it's skipped when
    #    LOCAL_OPERATOR=false (the deployed-pod CI default).
    echo 'if [ -f /.dockerenv ]; then unset GOROOT; export LOCAL_OPERATOR=true; fi'
  } >"${pc}.resolved"
  mv "${pc}.resolved" "${pc}"
fi

# switch_context.sh branches on EVR_TASK_ID: when set it sources the context
# chain directly (correct for CI); when unset it captures the chain's stdout
# and `eval`s it, so private-context's diagnostic echoes become bogus commands
# ("generating: command not found"). The container doesn't inherit EVR_TASK_ID,
# so forward it via a compose.user.yml environment override (reliable, same
# mechanism as MCK_DEVC_NET_PREFIX). It's an opaque id, YAML-safe unquoted.
# create_if_not_exists in initialize.sh won't clobber this pre-written file.
# GOSUMDB=off: modules download fine via GOPROXY inside the container, but the
# EVG host's outbound to sum.golang.org (the checksum DB) is blocked, so go's
# module verification fails ("open /go/pkg/sumdb/.../latest: no such file").
# Disabling the sumdb skips that verification (fine for throwaway CI).
cat >.devcontainer/compose.user.yml <<YAML
services:
  devcontainer:
    environment:
      EVR_TASK_ID: "${EVR_TASK_ID:-}"
      GOSUMDB: "off"
YAML

# Fresh artifact dir per task: under --in-place the worktree root is this
# checkout, so wt-ctl phase logs, test logs and gathered diagnostics all land in
# ./logs — which upload_e2e_logs archives to logs/${task_id}/${execution}/. On a
# task-group host shared across tasks, clear stale files so they aren't
# re-uploaded under the wrong task_id.
rm -rf logs && mkdir -p logs

header "wt-ctl create --local-kind --in-place ${TOPOLOGY_FLAG} (context=${CONTEXT})"
# --in-place: the CI patch diff is uncommitted in this checkout; a fresh git
# worktree would check out committed history only and miss it.
scripts/dev/wt-ctl create --local-kind --in-place "${TOPOLOGY_FLAG}" --context "${CONTEXT}" "${WT_NAME}"

# Cloud-QA org + API key: standard cloud_qa e2e tasks run setup_cloud_qa (via
# the `setup_cloud_qa` EVG function) to provision a scoped programmatic key and
# write OM_* into $ENV_FILE, which evg-private-context sources. We can't run it
# on the bare host (run_python.sh needs the in-container venv), so provision
# inside the container — it has the venv, requests, cloud-qa reachability, and
# the owner creds in context.env. ENV_FILE resolves to /workspace/.ops-manager-env
# (container workdir=/workspace). Then re-run prepare-local-e2e so
# configure_operator creates the my-credentials secret now that OM_* are set
# (the first create-phase run skipped it: OM_USER/OM_API_KEY were unset).
# OM-in-cluster contexts skip this entirely — the test deploys Ops Manager.
if [[ "${CLOUD_QA}" == "true" ]]; then
  header "provisioning cloud-qa org api key (setup_cloud_qa create)"
  scripts/dev/wt-ctl attach -- bash -lc \
    'export ops_manager_version=cloud_qa; scripts/dev/run_python.sh scripts/evergreen/e2e/setup_cloud_qa.py create'

  header "re-running prepare-local-e2e with OM credentials"
  scripts/dev/wt-ctl attach -- bash -lc "set -Eeuo pipefail; make switch context='${CONTEXT}'; make prepare-local-e2e"
fi

header "starting operator (go run) in devcontainer"
scripts/dev/wt-ctl attach -- bash scripts/dev/op_run.sh --detach

# All wt-ctl phases have succeeded by here; drop the phase-log dump trap so a
# plain test failure doesn't spam wt-ctl phase logs (diagnostics come from the
# gather step below instead).
trap - EXIT

# Run the test, but capture its status instead of aborting: diagnostics must be
# gathered on failure (mirroring CI e2e.sh) so upload_e2e_logs archives the same
# artifact set to S3. Gather runs inside the container while kind is still up.
header "running e2e marker: ${MARKER}"
set +e
scripts/dev/wt-ctl attach -- bash scripts/dev/e2e_run.sh "${MARKER}"
test_rc=$?
set -e

header "gathering diagnostics (rc=${test_rc})"
scripts/dev/wt-ctl attach -- bash -lc \
  "scripts/dev/e2e_gather_diagnostics.sh ${test_rc} '${MARKER}' '${build_variant:-e2e_mck_devcontainer_local_kind}'" || true

exit "${test_rc}"
