#!/usr/bin/env bats
# setup_kind_cluster.sh: where it writes the kubeconfig of the cluster it creates.
#
# `kind`, `kubectl` and `docker` are stubs on PATH.

load ../lib/fake_repo

setup() {
  make_fake_repo scripts/dev/setup_kind_cluster.sh
  # Like kind, the stub writes the file `kind create cluster --kubeconfig <file>` names.
  cat >"${STUB_BIN}/kind" <<'STUB'
#!/usr/bin/env bash
echo "kind $*" >>"${CALL_LOG}"
if [[ "$1 $2" == "create cluster" ]]; then
  while [[ $# -gt 0 ]]; do
    if [[ "$1" == "--kubeconfig" ]]; then mkdir -p "$(dirname "$2")" && echo "stub kubeconfig" >"$2"; fi
    shift
  done
  cat >/dev/null
fi
exit 0
STUB
  chmod +x "${STUB_BIN}/kind"
  stub_logger kubectl docker
}

# run_setup_kind_cluster <VAR=value>...
run_setup_kind_cluster() {
  printf 'export PROJECT_DIR=%s\nexport KUBE_ENVIRONMENT_NAME=kind\nexport CLUSTER_DOMAIN=cluster.local\n' \
    "${FAKE_REPO}" >"${FAKE_REPO}/.generated/context.export.env"
  run --separate-stderr env -u MCK_DEVC_LOCAL_KIND PATH="${STUB_BIN}:${PATH}" CALL_LOG="${CALL_LOG}" \
    HOME="${FAKE_HOME}" KIND_NODE_IMAGE="ghcr.io/example/node:v1" \
    "$@" bash -c 'cd "'"${FAKE_REPO}"'" && bash scripts/dev/setup_kind_cluster.sh -n foo'
}

@test "the cluster's kubeconfig goes to ~/.kube/<name> by default" {
  run_setup_kind_cluster

  assert_succeeded
  grep -q -- "^kind create cluster --name foo --kubeconfig ${FAKE_HOME}/.kube/foo " "${CALL_LOG}"
  [ ! -e "${FAKE_REPO}/.generated/kind-kubeconfigs" ]
}

@test "scoped mode writes the cluster's kubeconfig to .generated/kind-kubeconfigs and nothing under ~/.kube" {
  run_setup_kind_cluster MCK_DEVC_LOCAL_KIND=1

  assert_succeeded
  grep -q -- "^kind create cluster --name foo --kubeconfig ${FAKE_REPO}/.generated/kind-kubeconfigs/foo " "${CALL_LOG}"
  grep -q -- "^kubectl .*--kubeconfig ${FAKE_REPO}/.generated/kind-kubeconfigs/foo " "${CALL_LOG}"
  [ "$(grep -c -- "${FAKE_HOME}/.kube/foo" "${CALL_LOG}")" -eq 0 ]
  [ -f "${FAKE_REPO}/.generated/kind-kubeconfigs/foo" ]
  [ ! -e "${FAKE_HOME}/.kube" ]
}
