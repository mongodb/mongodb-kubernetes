#!/usr/bin/env bash

# Runs the e2e test of the mongodb-proxy mode (tests/replicaset/replica_set_mongodb_proxy.py) on a local kind
# cluster. Nothing here is released yet, so the script builds the proxy, the operator, the init-database image and
# a development agent image from the checked out branches and serves them from a local registry.
#
# Usage: scripts/dev/proxy_e2e.sh <command> [argument]
#
#   prereqs                 check the tools, the repositories and the context file
#   cluster                 create the kind cluster and the local registry (skipped when they exist, FORCE=1 recreates)
#   images [name]           build and push images: proxy, init, operator, agent or all (default)
#   namespace               create the namespace, the project ConfigMap, the credentials Secret, the CRDs and the
#                           operator installation ConfigMap
#   clean                   remove the leftovers of an earlier run from the namespace
#   test                    run the e2e test
#   rerun                   clean, then test
#   all                     prereqs, cluster, images, namespace, rerun
#   status                  show what exists
#   cleanup                 delete the namespace
#   destroy                 delete the cluster and the registry container (asks first)
#
# Settings (environment variables, all optional):
#   AGENT_REPO              the mms-automation checkout (default ~/work/mms-automation)
#   CLUSTER_NAME            kind cluster name (default proxy); the kubeconfig is ~/.kube/<name>
#   KIND_NODE_IMAGE         node image (default kindest/node:v1.36.1)
#   AGENT_BASE_VERSION      version of the released agent image the development image is built on
#   MDB_VERSION             MongoDB version of the test (default 8.0.4)
#   NEW_PROJECT=1           use a new Ops Manager project (needed when the MongoDB major version changes)
#   PROJECT_NAME            the Ops Manager project name; default: the one of the last run
#
# Image tags are unique per build and kept in a state file, because image volumes use IfNotPresent and the node would
# keep an older image that has the same tag.

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MCK="${MCK:-$(cd "${SCRIPT_DIR}/../.." && pwd)}"
AGENT_REPO="${AGENT_REPO:-${HOME}/work/mms-automation}"
CLUSTER_NAME="${CLUSTER_NAME:-proxy}"
KIND_NODE_IMAGE="${KIND_NODE_IMAGE:-kindest/node:v1.36.1}"
AGENT_BASE_VERSION="${AGENT_BASE_VERSION:-109.0.1.9310-1}"
MDB_VERSION="${MDB_VERSION:-8.0.4}"
OM_VERSION="${OM_VERSION:-9.0.1}"
NAMESPACE_NAME="mongodb-test"
REGISTRY_CONTAINER="kind-registry"
REGISTRY="localhost:5000"
REGISTRY_HOST_ADDR="http://127.0.0.1:5000"
STATE_DIR="${HOME}/.cache/proxy-e2e"
STATE_FILE="${STATE_DIR}/state"
LOG_FILE="${LOG_FILE:-/tmp/proxy-e2e.log}"

mkdir -p "${STATE_DIR}"
touch "${STATE_FILE}"

log() { printf '\n==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

kube() { kubectl --kubeconfig "${HOME}/.kube/${CLUSTER_NAME}" "$@"; }

state_get() {
  local value
  value="$(grep "^${1}=" "${STATE_FILE}" | tail -n 1 | cut -d= -f2- || true)"
  printf '%s' "${value}"
}

state_set() {
  local tmp
  tmp="$(mktemp)"
  grep -v "^${1}=" "${STATE_FILE}" >"${tmp}" || true
  printf '%s=%s\n' "${1}" "${2}" >>"${tmp}"
  mv "${tmp}" "${STATE_FILE}"
}

CONTEXT_FILE="${MCK}/.generated/context.env"

check_context() {
  [[ -f "${CONTEXT_FILE}" ]] || die "${CONTEXT_FILE} is missing: generate the dev context (e2e_mdb_kind_ubi_cloudqa)"
}

# The context file sets many variables that mean something else here (CLUSTER_NAME, REGISTRY, NAMESPACE, KUBECONFIG,
# ...), so it is only ever sourced in a subshell, never in the script itself.
# Reads the Ops Manager credentials into OM_HOST, OM_ORGID, OM_USER and OM_API_KEY. The values are never printed.
load_om_credentials() {
  check_context
  local assignments
  assignments="$(
    set +u
    set -a
    # shellcheck disable=SC1091
    source "${CONTEXT_FILE}" >/dev/null 2>&1
    set +a
    printf 'OM_HOST=%q OM_ORGID=%q OM_USER=%q OM_API_KEY=%q' "${OM_HOST:-}" "${OM_ORGID:-}" "${OM_USER:-}" "${OM_API_KEY:-}"
  )"
  eval "${assignments}"
  [[ -n "${OM_HOST}" && -n "${OM_ORGID}" && -n "${OM_USER}" && -n "${OM_API_KEY}" ]] ||
    die "the context file does not set OM_HOST, OM_ORGID, OM_USER and OM_API_KEY"
}

target_arch() {
  case "$(uname -m)" in
    arm64 | aarch64) echo arm64 ;;
    x86_64 | amd64) echo amd64 ;;
    *) die "unsupported machine: $(uname -m)" ;;
  esac
}

new_tag() { printf 'proxydev-%s' "$(date +%m%d%H%M%S)"; }

PREREQS_MISSING=0

check() {
  local description="${1}"
  shift
  if "$@" >/dev/null 2>&1; then
    echo "ok      ${description}"
  else
    echo "MISSING ${description}"
    PREREQS_MISSING=1
  fi
}

cmd_prereqs() {
  log "Checking prerequisites"
  PREREQS_MISSING=0
  local tool
  for tool in docker kind kubectl helm go; do
    check "${tool}" command -v "${tool}"
  done
  check "a running docker" docker info
  check "${MCK}/venv (scripts/dev/recreate_python_venv.sh)" test -x "${MCK}/venv/bin/pytest"
  check "${MCK}/.generated/context.env (the dev context e2e_mdb_kind_ubi_cloudqa)" test -f "${MCK}/.generated/context.env"
  check "the agent repo ${AGENT_REPO} (set AGENT_REPO)" test -d "${AGENT_REPO}/go_planner/src/com.tengen/cm"
  if [[ -d "${AGENT_REPO}/.git" ]]; then
    echo "        agent branch: $(git -C "${AGENT_REPO}" branch --show-current), MCK branch: $(git -C "${MCK}" branch --show-current)"
  fi
  [[ ${PREREQS_MISSING} -eq 0 ]] || die "fix the items above"
}

cmd_cluster() {
  if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}" && [[ "${FORCE:-0}" != "1" ]]; then
    log "Cluster ${CLUSTER_NAME} exists, skipping creation (FORCE=1 recreates it)"
  else
    log "Creating the kind cluster ${CLUSTER_NAME} with ${KIND_NODE_IMAGE}"
    # The script ends with an error in the CSI driver step (it looks for a context named <name>-kind). The test does
    # not need CSI, so only the state of the cluster is checked afterwards.
    (cd "${MCK}" && KIND_NODE_IMAGE="${KIND_NODE_IMAGE}" scripts/dev/recreate_kind_cluster.sh "${CLUSTER_NAME}") || true
  fi
  kind get clusters | grep -qx "${CLUSTER_NAME}" || die "the cluster ${CLUSTER_NAME} was not created"

  # The registry container must be on the kind network, or the nodes cannot resolve it.
  if [[ "$(docker inspect -f '{{json .NetworkSettings.Networks.kind}}' "${REGISTRY_CONTAINER}")" == "null" ]]; then
    docker network connect kind "${REGISTRY_CONTAINER}"
  fi
  kube wait --for=condition=Ready node --all --timeout=180s
  kube get nodes
}

build_proxy() {
  local tag
  tag="$(new_tag)"
  log "Building the proxy image ${REGISTRY}/mongodb-kubernetes-mongodb-proxy:${tag}"
  (cd "${MCK}" && make mongodb-proxy-image "PROXY_IMAGE=${REGISTRY}/mongodb-kubernetes-mongodb-proxy:${tag}" "TARGETARCH=$(target_arch)")
  docker push "${REGISTRY}/mongodb-kubernetes-mongodb-proxy:${tag}"
  state_set PROXY_TAG "${tag}"
}

build_init() {
  local tag
  tag="$(new_tag)"
  log "Building the init-database image ${tag}"
  (cd "${MCK}" && docker build -f docker/mongodb-kubernetes-init-database/Dockerfile --build-arg "version=${tag}" \
    -t "${REGISTRY}/mongodb-kubernetes-init-database:${tag}" .)
  docker push "${REGISTRY}/mongodb-kubernetes-init-database:${tag}"
  state_set INIT_TAG "${tag}"
}

build_operator() {
  local tag
  tag="$(new_tag)"
  log "Building the operator image ${tag}"
  (cd "${MCK}" && docker build -f docker/mongodb-kubernetes-operator/Dockerfile --build-arg "version=${tag}" \
    --build-arg "ldflag_version=${tag}" --build-arg log_automation_config_diff=false --build-arg use_race=false \
    -t "${REGISTRY}/mongodb-kubernetes:${tag}" .)
  docker push "${REGISTRY}/mongodb-kubernetes:${tag}"
  state_set OPERATOR_TAG "${tag}"
}

# The agent needs cgo (its metrics package enforces it) and that build has to run in Linux, so it runs in a Go
# container that borrows the module cache of the host, where the private github.com/10gen modules are.
build_agent() {
  local tag out modcache
  tag="$(new_tag)"
  out="$(mktemp -d)"
  modcache="$(cd "${AGENT_REPO}/go_planner/src/com.tengen/cm" && go env GOMODCACHE)"

  log "Building the agent (cgo) from $(git -C "${AGENT_REPO}" rev-parse --short HEAD) in ${AGENT_REPO}"
  docker run --rm -v "${AGENT_REPO}:/src:ro" -v "${modcache}:/go/pkg/mod:ro" -v "${out}:/out" \
    -w /src/go_planner/src/com.tengen/cm -e GOPROXY=off -e GOFLAGS=-mod=mod -e 'GONOSUMDB=*' \
    public.ecr.aws/docker/library/golang:1.26 \
    sh -c 'CGO_ENABLED=1 go build -buildvcs=false -tags=mongointernal -o /out/automation-agent ./main'

  # docker/mongodb-agent has the legacy scripts, which wait for the utilities container that a proxy pod does not have.
  # These copies read the init-database image volume (MDB_INIT_ROOT); see the README there.
  cp "${SCRIPT_DIR}/proxy_e2e_agent/setup-agent-files.sh" "${SCRIPT_DIR}/proxy_e2e_agent/dummy-probe.sh" \
    "${SCRIPT_DIR}/proxy_e2e_agent/dummy-readinessprobe" "${out}/"
  cat >"${out}/Dockerfile" <<'EOF'
ARG BASE_IMAGE
FROM ${BASE_IMAGE}
USER root
COPY automation-agent /agent/mongodb-agent
COPY setup-agent-files.sh /usr/local/bin/setup-agent-files.sh
COPY dummy-probe.sh /usr/local/bin/dummy-probe.sh
COPY dummy-readinessprobe /usr/local/bin/dummy-readinessprobe
RUN chmod +x /agent/mongodb-agent /usr/local/bin/setup-agent-files.sh /usr/local/bin/dummy-probe.sh /usr/local/bin/dummy-readinessprobe
# the cgo-free fallback of os/user needs these for the arbitrary uid of the pod; harmless with a cgo build
ENV USER=mongodb HOME=/tmp
USER 2000
EOF
  log "Building the development agent image on quay.io/mongodb/mongodb-agent:${AGENT_BASE_VERSION}"
  docker build --build-arg "BASE_IMAGE=quay.io/mongodb/mongodb-agent:${AGENT_BASE_VERSION}" \
    -t "${REGISTRY}/mongodb-agent:${AGENT_BASE_VERSION}-${tag}" "${out}"
  docker push "${REGISTRY}/mongodb-agent:${AGENT_BASE_VERSION}-${tag}"
  # the operator takes the agent image of the Ops Manager AppDB from the same registry, under the released tag
  docker tag "quay.io/mongodb/mongodb-agent:${AGENT_BASE_VERSION}" "${REGISTRY}/mongodb-agent:${AGENT_BASE_VERSION}"
  docker push "${REGISTRY}/mongodb-agent:${AGENT_BASE_VERSION}"
  state_set AGENT_TAG "${AGENT_BASE_VERSION}-${tag}"
  rm -rf "${out}"
}

cmd_images() {
  curl -fs "${REGISTRY_HOST_ADDR}/v2/_catalog" >/dev/null || die "the registry at ${REGISTRY_HOST_ADDR} does not answer (run: cluster)"
  case "${1:-all}" in
    proxy) build_proxy ;;
    init) build_init ;;
    operator) build_operator ;;
    agent) build_agent ;;
    all) build_proxy; build_init; build_operator; build_agent ;;
    *) die "unknown image '${1}': proxy, init, operator, agent or all" ;;
  esac
  log "Images in the registry"
  curl -s "${REGISTRY_HOST_ADDR}/v2/_catalog"; echo
  cat "${STATE_FILE}"
}

require_tags() {
  local key
  for key in PROXY_TAG INIT_TAG OPERATOR_TAG AGENT_TAG; do
    [[ -n "$(state_get "${key}")" ]] || die "no ${key} in ${STATE_FILE}: run '$0 images' first"
  done
}

apply_from_create() {
  # kubectl create ... --dry-run=client -o yaml | kubectl apply: creates or replaces, never prints the values
  kube "$@" --dry-run=client -o yaml | kube apply -f - >/dev/null
}

cmd_namespace() {
  require_tags
  load_om_credentials

  local project
  project="${PROJECT_NAME:-}"
  if [[ -z "${project}" && "${NEW_PROJECT:-0}" != "1" ]]; then
    project="$(state_get PROJECT_NAME)"
  fi
  if [[ -z "${project}" ]]; then
    project="${NAMESPACE_NAME}-proxy-$(date +%m%d%H%M)"
  fi
  state_set PROJECT_NAME "${project}"
  log "Namespace ${NAMESPACE_NAME}, Ops Manager project ${project}"

  kube create namespace "${NAMESPACE_NAME}" --dry-run=client -o yaml | kube apply -f - >/dev/null
  # the admin of an Ops Manager of the operator (OM_TEST=1), the values of scripts/evergreen/e2e/configure_operator.sh
  apply_from_create -n "${NAMESPACE_NAME}" create secret generic ops-manager-admin-secret \
    --from-literal Username=jane.doe@example.com --from-literal Password='Passw0rd.' \
    --from-literal FirstName=Jane --from-literal LastName=Doe
  apply_from_create -n "${NAMESPACE_NAME}" create configmap my-project \
    --from-literal "orgId=${OM_ORGID}" --from-literal "projectName=${project}" --from-literal "baseUrl=${OM_HOST}"
  apply_from_create -n "${NAMESPACE_NAME}" create secret generic my-credentials \
    --from-literal "user=${OM_USER}" --from-literal "publicApiKey=${OM_API_KEY}"
  kube apply --server-side --force-conflicts -f "${MCK}/helm_chart/crds" >/dev/null
  kube -n "${NAMESPACE_NAME}" delete configmap operator-installation-config --ignore-not-found >/dev/null
  kube -n "${NAMESPACE_NAME}" create configmap operator-installation-config \
    --from-literal "registry.operator=${REGISTRY}" --from-literal "operator.version=$(state_get OPERATOR_TAG)" \
    --from-literal "registry.initDatabase=${REGISTRY}" --from-literal "initDatabase.version=$(state_get INIT_TAG)" \
    --from-literal "registry.agent=${REGISTRY}" --from-literal "agent.version=${AGENT_BASE_VERSION}" \
    --from-literal "registry.opsManager=${REGISTRY}" --from-literal registry.pullPolicy=IfNotPresent \
    --from-literal operator.mdbDefaultArchitecture=static \
    --from-literal operator.telemetry.enabled=false --from-literal operator.telemetry.send.enabled=false \
    --from-literal operator.telemetry.collection.clusters.enabled=false \
    --from-literal operator.telemetry.collection.deployments.enabled=false \
    --from-literal operator.telemetry.collection.operators.enabled=false >/dev/null
  log "Done"
}

cmd_clean() {
  log "Cleaning the leftovers in ${NAMESPACE_NAME}"
  # a user of an earlier run would enable authentication again in the project, whatever the new resource asks for
  kube -n "${NAMESPACE_NAME}" delete mongodbusers --all --ignore-not-found --timeout=120s || true
  # wait for the resource to disappear: the test loads an existing resource, and one that is still being deleted
  # brings the spec of the earlier run into the new one
  kube -n "${NAMESPACE_NAME}" delete mdb my-replica-set --ignore-not-found --timeout=180s || true
  kube -n "${NAMESPACE_NAME}" delete pod my-replica-set-0 my-replica-set-1 my-replica-set-2 \
    --grace-period=0 --force --ignore-not-found 2>&1 | grep -v "^Warning" || true
  # logs and data live on the PVCs and survive pod restarts
  kube -n "${NAMESPACE_NAME}" delete pvc --all --wait=false || true
  # an earlier run scaled the operator itself, and a helm upgrade then fails with an apply conflict
  kube -n "${NAMESPACE_NAME}" delete deploy mongodb-kubernetes-operator --ignore-not-found || true
}

cmd_test() {
  require_tags
  check_context
  local proxy_image agent_image kubeconfig rc marker test_file
  # OM_TEST=1 runs the test with an Ops Manager of the operator (tests/opsmanager/om_appdb_mongodb_proxy.py) instead of
  # the one with Cloud Manager. Its image is amd64 only, see the memory note about importing it into the kind node.
  if [[ "${OM_TEST:-}" == "1" ]]; then
    marker=e2e_om_appdb_mongodb_proxy
    test_file=docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_mongodb_proxy.py
  else
    marker=e2e_replica_set_mongodb_proxy
    test_file=docker/mongodb-kubernetes-tests/tests/replicaset/replica_set_mongodb_proxy.py
  fi
  proxy_image="${REGISTRY}/mongodb-kubernetes-mongodb-proxy:$(state_get PROXY_TAG)"
  agent_image="${REGISTRY}/mongodb-agent:$(state_get AGENT_TAG)"
  kubeconfig="${HOME}/.kube/${CLUSTER_NAME}"
  log "Running the test with ${proxy_image} and ${agent_image} (log: ${LOG_FILE})"

  set +e
  (
    cd "${MCK}"
    # the test harness reads the Ops Manager settings and more from the context
    set +u
    set -a
    # shellcheck disable=SC1091
    source "${CONTEXT_FILE}" >/dev/null 2>&1
    set +a
    set -u
    export KUBECONFIG="${kubeconfig}" NAMESPACE="${NAMESPACE_NAME}" MDB_DEFAULT_ARCHITECTURE=static LOCAL_OPERATOR=true \
      "CUSTOM_MDB_VERSION=${MDB_VERSION}" "CUSTOM_OM_VERSION=${OM_VERSION}" "MDB_MONGODB_PROXY_IMAGE=${proxy_image}" \
      "MDB_PROXY_TEST_AGENT_IMAGE=${agent_image}" PYTHONPATH=docker/mongodb-kubernetes-tests
    # PYTEST_K=<expression> runs only the matching tests (pytest -k), against the resource of an earlier run
    venv/bin/pytest -m "${marker}" "${test_file}" \
      ${PYTEST_K:+-k "${PYTEST_K}"} --disable-pytest-warnings -s -p no:cacheprovider 2>&1 | tee "${LOG_FILE}"
    exit "${PIPESTATUS[0]}"
  )
  rc=$?
  set -e

  log "Result (exit code ${rc})"
  sed 's/\x1b\[[0-9;]*m//g' "${LOG_FILE}" | grep -E "^(PASSED|FAILED|ERROR) |passed|failed" | tail -n 12 || true
  return "${rc}"
}

cmd_status() {
  log "Cluster"
  kind get clusters 2>&1 || true
  docker ps --format '{{.Names}} {{.Status}}' 2>&1 | grep -E "proxy|registry" || true
  log "Registry"
  curl -s "${REGISTRY_HOST_ADDR}/v2/_catalog" || true; echo
  log "State file ${STATE_FILE}"
  cat "${STATE_FILE}"
  log "Namespace ${NAMESPACE_NAME}"
  kube -n "${NAMESPACE_NAME}" get pods,mdb 2>&1 || true
}

cmd_cleanup() {
  log "Deleting the namespace ${NAMESPACE_NAME}"
  kube delete namespace "${NAMESPACE_NAME}" --ignore-not-found --wait=false
  echo "The Ops Manager project '$(state_get PROJECT_NAME)' in cloud-qa is not deleted. Delete it by hand."
}

cmd_destroy() {
  read -r -p "Delete the kind cluster ${CLUSTER_NAME} and the container ${REGISTRY_CONTAINER}? [y/N] " answer
  [[ "${answer}" == "y" ]] || die "aborted"
  kind delete cluster --name "${CLUSTER_NAME}"
  docker rm -f "${REGISTRY_CONTAINER}" >/dev/null 2>&1 || true
  rm -f "${HOME}/.kube/${CLUSTER_NAME}"
}

main() {
  local command="${1:-}"
  local argument="${2:-}"
  case "${command}" in
    prereqs) cmd_prereqs ;;
    cluster) cmd_cluster ;;
    images) cmd_images "${argument:-all}" ;;
    namespace) cmd_namespace ;;
    clean) cmd_clean ;;
    test) cmd_test ;;
    rerun) cmd_clean; cmd_test ;;
    all) cmd_prereqs; cmd_cluster; cmd_images all; cmd_namespace; cmd_clean; cmd_test ;;
    status) cmd_status ;;
    cleanup) cmd_cleanup ;;
    destroy) cmd_destroy ;;
    *) sed -n '3,/^set -Eeuo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'; [[ -z "${command}" ]] || exit 1 ;;
  esac
}

main "$@"
