#!/usr/bin/env bash

# On-demand CI of the devcontainer worktrees, run on the task host of e2e_mck_devcontainer_local_kind.
#
# Without a marker (task e2e_devc_worktrees) it runs the wt-ctl unit tests and the two integration tests that create kind
# clusters. With e2e_marker, it creates a wt-ctl worktree with local kind clusters and runs the marker in its container,
# with the operator started by `go run` there. No images are built: they resolve to the :latest tags unless
# OVERRIDE_VERSION_ID pins a build.

set -Eeou pipefail

source scripts/funcs/printing

MARKER="${e2e_marker:-}"
CONTEXT="${e2e_context:-e2e_multi_cluster_kind}"
DEVCONTAINER_CLI_VERSION="0.87.0"
mkdir -p logs
checkout_logs="${PWD}/logs"

# The step logs of a failed worktree are not uploaded, so print their tails; copy the worktree's logs to the checkout's
# logs/, which upload_e2e_logs reads (the script runs in the worktree's directory by then).
collect_logs() {
  local rc=$?
  if [[ -n "${worktree_dir:-}" && -d "${worktree_dir}/logs" ]]; then
    if [[ ${rc} -ne 0 ]]; then
      for f in "${worktree_dir}"/logs/setup_worktree/*.log; do
        [[ -f "${f}" ]] && { echo "===== tail -n 100 ${f}"; tail -n 100 "${f}"; }
      done
    fi
    find "${worktree_dir}/logs" -type f -exec cp -t "${checkout_logs}" {} +
  fi
  chmod -R u+w "${GOPATH:-/nonexistent}" 2>/dev/null || true
  return "${rc}"
}
trap collect_logs EXIT

# The devcontainer CLI needs Node 18 or newer, and the host image has an older one.
# devenv replays the PATH that the last `make switch` recorded, before this install, so the wt-ctl that a test starts
# would not find the tools. It always puts ./bin on the PATH (bin/* is gitignored), so link them there.
ci_tools="${PWD}/.generated/ci-tools"
mkdir -p bin
# `make` runs `go env` in every checkout, and the first go command on a host writes ~/go (the module cache, with the
# Go toolchain that go.mod asks for) and ~/.config/go (telemetry). wt_ctl_integration checks that creating worktrees
# leaves the home directory alone, so keep both in the temp directory of the task, outside .generated/ because every
# worktree copies that. The module cache is read-only, which the workdir cleanup of the agent cannot remove, so the
# trap makes it writable again.
export GOPATH="${TMPDIR:-/tmp}/devc-go" XDG_CONFIG_HOME="${TMPDIR:-/tmp}/devc-config"
if ! command -v node >/dev/null || (( $(node -p 'process.versions.node.split(".")[0]') < 18 )); then
  header "installing Node"
  case "$(uname -m)" in aarch64 | arm64) arch="arm64" ;; *) arch="x64" ;; esac
  mkdir -p "${ci_tools}/node"
  curl -fsSL "https://nodejs.org/dist/v22.14.0/node-v22.14.0-linux-${arch}.tar.xz" |
    tar -xJ -C "${ci_tools}/node" --strip-components=1
  export PATH="${ci_tools}/node/bin:${PATH}"
  ln -sf "${ci_tools}/node/bin/node" bin/node
fi
if ! command -v devcontainer >/dev/null; then
  header "installing the devcontainer CLI"
  export npm_config_prefix="${ci_tools}/npm-global" npm_config_cache="${ci_tools}/npm-cache"
  npm i -g "@devcontainers/cli@${DEVCONTAINER_CLI_VERSION}"
  export PATH="${npm_config_prefix}/bin:${PATH}"
  ln -sf "${npm_config_prefix}/bin/devcontainer" bin/devcontainer
fi

# Without a pin, evg-private-context tags the images with the commit and pods fail to pull them.
IMAGE_VERSIONS=(OPERATOR_VERSION READINESS_PROBE_VERSION VERSION_UPGRADE_HOOK_VERSION DATABASE_VERSION
  INIT_DATABASE_VERSION INIT_OPS_MANAGER_VERSION)
if [[ -z "${OVERRIDE_VERSION_ID:-}" ]]; then
  export BUILD_SCENARIO=staging
  for v in "${IMAGE_VERSIONS[@]}"; do export "${v}=latest"; done
fi

if [[ -n "${MARKER}" ]]; then
  # The context says whether the worktree is multi-cluster and whether Cloud QA provides Ops Manager.
  header "switching to context ${CONTEXT}"
  make switch context="${CONTEXT}" >/dev/null
  source scripts/dev/set_env_context.sh
fi

# The containers run `make switch` too, and evg-private-context reads Evergreen expansions that only this shell
# has. The checkout is mounted, so write their values into private-context.
pc="scripts/dev/contexts/private-context"
header "resolving the Evergreen expansions into private-context"
{
  for v in EVR_TASK_ID mms_eng_test_aws_access_key mms_eng_test_aws_secret mms_eng_test_aws_region \
    e2e_cloud_qa_apikey_owner_ubi_cloudqa e2e_cloud_qa_orgid_owner_ubi_cloudqa e2e_cloud_qa_user_owner_ubi_cloudqa \
    cognito_user_name cognito_user_password cognito_user_pool_id cognito_workload_federation_client_id \
    cognito_workload_federation_client_secret cognito_workload_url cognito_workload_user_id \
    community_private_preview_pullsecret_dockerconfigjson; do
    printf 'export %s=%q\n' "${v}" "${!v:-}"
  done
  cat "${pc}"
  # The operator runs in the container, so prepare-local-e2e has to build the member list it reads.
  echo 'if [ -f /.dockerenv ]; then export LOCAL_OPERATOR=true; fi'
} >"${pc}.resolved"
mv "${pc}.resolved" "${pc}"

# What the container needs of this shell, as compose.user.yml (every worktree copies the one of this checkout).
# EVR_TASK_ID makes switch_context.sh source the context chain directly, as it does on CI. GOSUMDB=off because the
# host cannot reach sum.golang.org. workdir is the only expansion that evg-private-context reads without a default and
# that is not in private-context: it holds a host path there, so the container gets a directory of its own for the
# namespace file and the Cloud QA env file.
{
  printf 'services:\n  devcontainer:\n    environment:\n      GOSUMDB: "off"\n'
  printf '      workdir: "/workspace/.generated"\n'
  for v in EVR_TASK_ID BUILD_SCENARIO OVERRIDE_VERSION_ID "${IMAGE_VERSIONS[@]}"; do
    if [[ -n "${!v:-}" ]]; then printf '      %s: "%s"\n' "${v}" "${!v}"; fi
  done
} >.devcontainer/compose.user.yml

# wt-ctl and the tests add worktrees of HEAD, and Evergreen applies a patch to the working tree only.
git add -A
git diff --cached --quiet || git commit -q -m "Patched tree of the task"

if [[ -z "${MARKER}" ]]; then
  header "wt-ctl unit tests, kind prefixes and wt-ctl worktrees"
  scripts/dev/run_python.sh -m pytest -v --junitxml=logs/myreport.xml scripts/dev/wt_ctl/tests \
    scripts/test/kind_prefix_integration.py scripts/test/wt_ctl_integration.py
  exit
fi

topology="--single-cluster"
[[ "${KUBE_ENVIRONMENT_NAME:-kind}" == "multi" ]] && topology="--multi-cluster"
worktree="ci-${MARKER//_/-}"
worktree_dir="$(realpath "${PWD}/..")/${worktree}"

header "wt-ctl create --local-kind ${topology} (context ${CONTEXT})"
scripts/dev/wt-ctl create --local-kind "${topology}" --context "${CONTEXT}" "${worktree}"
cd "${worktree_dir}"
in_container() { scripts/dev/wt-ctl attach -- bash -lc "cd /workspace && $1"; }

# Cloud QA contexts need an organization and an API key, which setup_cloud_qa.py makes in the container, where the
# venv is. prepare-local-e2e ran before they existed, so it runs again for the my-credentials secret.
if [[ "${ops_manager_version:-}" == "cloud_qa" ]]; then
  header "provisioning Cloud QA and preparing the clusters again"
  in_container "ops_manager_version=cloud_qa scripts/dev/run_python.sh scripts/evergreen/e2e/setup_cloud_qa.py create"
  in_container "make switch context='${CONTEXT}' && make prepare-local-e2e"
fi

header "starting the operator and running ${MARKER}"
in_container "scripts/dev/op_run.sh --detach"
in_container "scripts/dev/e2e_run.sh ${MARKER}"
