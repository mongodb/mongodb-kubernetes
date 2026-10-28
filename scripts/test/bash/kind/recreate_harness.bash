# Shared by the bats suites of the kind scripts in this directory. The scripts run in a
# scratch checkout with stubs for docker, aws, kind, kubectl, istioctl, make and sleep on PATH
# that log their argv to CALL_LOG, so no daemon runs. The helpers the recreate scripts call
# (setup_kind_cluster.sh, interconnect, istio, csi) are stubs too, unless a test swaps in the
# real file with use_real. Every stub helper and `kind export kubeconfig` log the KUBECONFIG
# they run with to KUBECONFIG_LOG.

load ../lib/fake_repo

recreate_setup() {
  make_fake_repo scripts/dev/recreate_kind_cluster.sh scripts/dev/recreate_kind_clusters.sh
  ENV_LOG="${BATS_TEST_TMPDIR}/env.log"
  KUBECONFIG_LOG="${BATS_TEST_TMPDIR}/kubeconfig.log"
  mkdir -p "${FAKE_REPO}/multi_cluster/tools" "${FAKE_HOME}/.kube" "${FAKE_REPO}/istio-1.16.1/bin"
  : >"${ENV_LOG}"
  : >"${KUBECONFIG_LOG}"
  # What the last `make switch` baked. Like evg-private-context, the context exports
  # DELETE_KIND_NETWORK=true and, like root-context, the user's default KUBECONFIG.
  printf 'export PROJECT_DIR=%s\nexport CLUSTER_DOMAIN=cluster.local\nexport DELETE_KIND_NETWORK=true\nexport KUBECONFIG=%s\n' \
    "${FAKE_REPO}" "${FAKE_HOME}/.kube/config" >"${FAKE_REPO}/.generated/context.export.env"

  local helper
  for helper in scripts/dev/interconnect_kind_clusters.sh \
    multi_cluster/tools/install_istio.sh multi_cluster/tools/install_istio_central.sh; do
    printf '#!/usr/bin/env bash\nsource scripts/dev/set_env_context.sh\necho "%s $*" >>"${CALL_LOG}"\necho "${KUBECONFIG}" >>"${KUBECONFIG_LOG}"\n' \
      "${helper}" >"${FAKE_REPO}/${helper}"
    chmod +x "${FAKE_REPO}/${helper}"
  done
  # setup_kind_cluster.sh logs its argv and the KUBECONFIG it ends up with, and the docker
  # network variable kind would read. It writes the per-cluster kubeconfig like kind create.
  cat >"${FAKE_REPO}/scripts/dev/setup_kind_cluster.sh" <<'STUB'
#!/usr/bin/env bash
source scripts/dev/set_env_context.sh
source scripts/funcs/kind_network
echo "scripts/dev/setup_kind_cluster.sh $*" >>"${CALL_LOG}"
echo "${KUBECONFIG}" >>"${KUBECONFIG_LOG}"
echo "setup_kind_cluster.sh KIND_EXPERIMENTAL_DOCKER_NETWORK=${KIND_EXPERIMENTAL_DOCKER_NETWORK:-<unset>}" >>"${ENV_LOG}"
while getopts "n:p:s:l:c:rek" opt; do
  case "${opt}" in
    n) name="${OPTARG}" ;;
    *) ;;
  esac
done
source scripts/funcs/kubernetes
printf 'clusters:\n- cluster:\n    server: https://127.0.0.1:35765\n  name: kind-%s\ncurrent-context: kind-%s\n' \
  "${name}" "${name}" >"$(kind_cluster_kubeconfig "${name}")"
STUB
  chmod +x "${FAKE_REPO}/scripts/dev/setup_kind_cluster.sh"
  printf '#!/usr/bin/env bash\n: # the istio download is a no-op in these tests\n' \
    >"${FAKE_REPO}/multi_cluster/tools/download_istio.sh"
  printf 'source scripts/dev/set_env_context.sh\ncsi_driver_download() { :; }\ncsi_driver_deploy() { echo "csi ${1}" >>"${CALL_LOG}"; echo "${KUBECONFIG}" >>"${KUBECONFIG_LOG}"; }\n' \
    >"${FAKE_REPO}/scripts/dev/install_csi_driver.sh"
  printf '#!/usr/bin/env bash\necho "istioctl $*" >>"${CALL_LOG}"\n' >"${FAKE_REPO}/istio-1.16.1/bin/istioctl"
  chmod +x "${FAKE_REPO}/istio-1.16.1/bin/istioctl"

  stub_logger aws make sleep
  cat >"${STUB_BIN}/kind" <<'STUB'
#!/usr/bin/env bash
echo "kind $*" >>"${CALL_LOG}"
case "$1 $2" in
  "get nodes") echo "${4}-control-plane" ;;
  "export kubeconfig")
    echo "${KUBECONFIG}" >>"${KUBECONFIG_LOG}"
    # Kind merges the cluster, context and user entry called kind-<name> into $KUBECONFIG and
    # makes it the current-context. The stub keeps the file in kind's layout and merges by section.
    name="kind-$4"
    section() { [[ -s "${KUBECONFIG}" ]] && awk -v s="$1:" '/^[a-z-]+:/ {cur=$1} cur==s && !/^[a-z-]+:/ {print}' "${KUBECONFIG}"; true; }
    {
      echo "apiVersion: v1"
      echo "clusters:"; section clusters
      printf -- '- cluster:\n    certificate-authority-data: LS0tQ0FEQVRB\n    server: https://127.0.0.1:35765\n  name: %s\n' "${name}"
      echo "contexts:"; section contexts
      printf -- '- context:\n    cluster: %s\n    user: %s\n  name: %s\n' "${name}" "${name}" "${name}"
      echo "current-context: ${name}"
      echo "kind: Config"
      echo "users:"; section users
      printf -- '- name: %s\n  user:\n    client-certificate-data: LS0tQ0VSVA==\n    client-key-data: LS0tS0VZ\n' "${name}"
    } >"${KUBECONFIG}.new"
    mv "${KUBECONFIG}.new" "${KUBECONFIG}"
    ;;
esac
exit 0
STUB
  # The answers interconnect_kind_clusters.sh and install_istio.sh parse.
  cat >"${STUB_BIN}/kubectl" <<'STUB'
#!/usr/bin/env bash
echo "kubectl $*" >>"${CALL_LOG}"
case "$*" in
  *"get nodes -o=jsonpath"*) echo "ip route add 10.244.0.0/24 via 172.18.0.2" ;;
  *"get configmap kubeadm-config"*) echo "  serviceSubnet: 10.96.0.0/16" ;;
  *"get node "*) echo "172.18.0.5" ;;
esac
exit 0
STUB
  chmod +x "${STUB_BIN}/kind" "${STUB_BIN}/kubectl"
  cat >"${STUB_BIN}/docker" <<'STUB'
#!/usr/bin/env bash
echo "docker $*" >>"${CALL_LOG}"
case "$1 $2" in
  # The node image is never cached, so an ECR image always needs a login.
  "image inspect") exit 1 ;;
  "network ls") echo "abc123 kind bridge local" ;;
  "network inspect")
    if [[ "$3" == "-f" ]]; then
      # The format tells the two callers apart: IPAM subnets or the containers.
      case "$4" in
        *IPAM*) echo "${DOCKER_STUB_NET_SUBNET:-}" ;;
        *) [[ -z "${DOCKER_STUB_NET_CONTAINERS:-}" ]] || echo "${DOCKER_STUB_NET_CONTAINERS}" ;;
      esac
    else
      # Absent network unless the test gives it a subnet.
      [[ -n "${DOCKER_STUB_NET_SUBNET:-}" ]] || exit 1
    fi
    ;;
  "inspect -f")
    # After a `docker run` the registry may be up (another setup won the start race).
    if [[ -e "${CALL_LOG}.ran" ]]; then echo "${DOCKER_STUB_REGISTRY_RUNNING_AFTER_RUN:-${DOCKER_STUB_REGISTRY_RUNNING:-}}"
    else echo "${DOCKER_STUB_REGISTRY_RUNNING:-}"; fi
    ;;
  "run -d")
    touch "${CALL_LOG}.ran"
    [[ "${DOCKER_STUB_RUN_FAILS:-}" != 1 ]] || { echo "name conflict" >&2; exit 125; }
    ;;
esac
exit 0
STUB
  chmod +x "${STUB_BIN}/docker"
}

# Replaces a stubbed helper of the scratch repo with the real script.
use_real() {
  cp "${REPO_ROOT}/$1" "${FAKE_REPO}/$1"
}

# Runs a command in the scratch repo in a fresh bash. Arguments up to `--` are
# VAR=value assignments for that process; the rest is the command. The variables
# under test start unset, whatever the caller's shell exports.
run_in_fake_repo() {
  local assignments=()
  while [[ "$1" != "--" ]]; do assignments+=("$1"); shift; done
  shift
  run --separate-stderr env -u KIND_LB_PREFIX -u KIND_NETWORK_SUBNET -u MCK_DEVC_LOCAL_KIND \
    -u MCK_DEVC_HOST_INDEX -u MCK_DEVC_NET_X -u MCK_DEVC_NET_Y_BASE -u KIND_NAME_PREFIX -u KIND_EXPERIMENTAL_DOCKER_NETWORK \
    PATH="${STUB_BIN}:${PATH}" CALL_LOG="${CALL_LOG}" ENV_LOG="${ENV_LOG}" KUBECONFIG_LOG="${KUBECONFIG_LOG}" HOME="${FAKE_HOME}" \
    KIND_NODE_IMAGE="ghcr.io/example/node:v1" \
    ${assignments[@]+"${assignments[@]}"} bash -c 'cd "'"${FAKE_REPO}"'" && "$@"' _ "$@"
}

# run_in_fake_repo for a command that must exit 0.
run_in_fake_repo_ok() {
  run_in_fake_repo "$@"
  assert_succeeded
}

# Compares the sorted call log with fixtures/argv-<scenario>.log.
assert_golden() {
  local fixture="${BATS_TEST_DIRNAME}/fixtures/argv-$1.log" actual="${BATS_TEST_TMPDIR}/actual.log"
  sed "s#${FAKE_REPO}#<repo>#g" "${CALL_LOG}" | LC_ALL=C sort >"${actual}"
  if [[ "${GOLDEN_RECORD:-}" == 1 ]]; then
    mkdir -p "${BATS_TEST_DIRNAME}/fixtures"
    cp "${actual}" "${fixture}"
    return 0
  fi
  diff -u "${fixture}" "${actual}"
}
