#!/usr/bin/env bats
# The kind scripts with a stack index (MCK_DEVC_HOST_INDEX=N): several kind setups on one
# docker host, each with its own docker network, subnet and cluster names. With the index
# unset, the commands of the recreate scripts, the network and the registry are pinned in
# test_kind_scripts.bats. The harness (recreate_harness.bash) says what is real and what
# is a stub.

load recreate_harness

setup() {
  recreate_setup
}

@test "without an index setup_kind_cluster.sh sees no docker network variable" {
  run_in_fake_repo_ok MCK_DEVC_LOCAL_KIND=1 -- bash scripts/dev/recreate_kind_clusters.sh
  [ "$(grep -c . "${ENV_LOG}")" -eq 5 ]
  [ "$(sort -u "${ENV_LOG}")" = "setup_kind_cluster.sh KIND_EXPERIMENTAL_DOCKER_NETWORK=<unset>" ]
}

# --- Stack index 17: devc17, 172.16.34.0/23 --------------------------------------

# Stack 17: its index and the second and third address octet its caller picked.
STACK_17=(MCK_DEVC_HOST_INDEX=17 MCK_DEVC_NET_X=16 MCK_DEVC_NET_Y_BASE=34)

STACK_CLUSTERS=(e2e-operator e2e-cluster-1 e2e-cluster-2 e2e-cluster-3 kind)

# Counts the cluster, context and user entries called <name> in a kubeconfig.
count_entries() { # <name> <file>
  grep -Ec -- "^(  |- )name: $1\$" "$2" || true
}

@test "stack index 17: the five clusters are created, exported and deleted under the devc17- prefix and nothing else is touched" {
  run_in_fake_repo_ok MCK_DEVC_LOCAL_KIND=1 "${STACK_17[@]}" -- bash scripts/dev/recreate_kind_clusters.sh
  for name in "${STACK_CLUSTERS[@]}"; do
    [ "$(calls_matching "^scripts/dev/setup_kind_cluster.sh -n devc17-${name} ")" -eq 1 ]
    [ "$(calls_matching "^kind export kubeconfig --name devc17-${name}\$")" -eq 1 ]
    [ "$(calls_matching "^kind delete cluster --name devc17-${name}\$")" -eq 1 ]
  done
  [ "$(calls_matching '^kind delete cluster ')" -eq 5 ]
  [ "$(calls_matching 'kind delete clusters --all')" -eq 0 ]
  [ "$(calls_matching 'prune')" -eq 0 ]
  [ "$(calls_matching '^docker stop')" -eq 0 ]
  [ "$(calls_matching '^docker network rm')" -eq 0 ]
}

@test "stack index 17: the MetalLB ranges are on the stack's /23 and kind joins the devc17 network" {
  run_in_fake_repo_ok MCK_DEVC_LOCAL_KIND=1 "${STACK_17[@]}" -- bash scripts/dev/recreate_kind_clusters.sh
  grep -q -- '-n devc17-e2e-operator .* -l 172.16.34.200-172.16.34.209 ' "${CALL_LOG}"
  grep -q -- '-n devc17-e2e-cluster-3 .* -l 172.16.34.230-172.16.34.239 ' "${CALL_LOG}"
  grep -q -- '-n devc17-kind -l 172.16.34.240-172.16.34.249 ' "${CALL_LOG}"
  [ "$(grep -c . "${ENV_LOG}")" -eq 5 ]
  [ "$(sort -u "${ENV_LOG}")" = "setup_kind_cluster.sh KIND_EXPERIMENTAL_DOCKER_NETWORK=devc17" ]
}

@test "stack index 17: the docker network is devc17 on the stack's /23 with nodes in its lower /25" {
  run_in_fake_repo_ok "${STACK_17[@]}" -- bash -c 'source scripts/funcs/kubernetes && docker_create_kind_network'
  grep -qx 'docker network inspect devc17' "${CALL_LOG}"
  grep -qx 'docker network create --subnet=172.16.34.0/23 --ip-range=172.16.34.0/25 --label mck.devc.index=17 devc17' "${CALL_LOG}"
}

@test "stack index 17: an existing devc17 network with the stack's subnet is reused" {
  run_in_fake_repo_ok "${STACK_17[@]}" DOCKER_STUB_NET_SUBNET="172.16.34.0/23 " -- \
    bash -c 'source scripts/funcs/kubernetes && docker_create_kind_network'
  [ "$(calls_matching '^docker network create')" -eq 0 ]
}

@test "stack index 17: an existing devc17 network with another subnet is an error that names the network" {
  run_in_fake_repo "${STACK_17[@]}" DOCKER_STUB_NET_SUBNET="10.9.0.0/16 " -- \
    bash -c 'source scripts/funcs/kubernetes && docker_create_kind_network'

  [ "$status" -ne 0 ]
  [[ "${stderr}" == *"docker network 'devc17' exists with subnet(s) '10.9.0.0/16'"* ]]
  [ "$(calls_matching '^docker network create')" -eq 0 ]
}

@test "stack index: kind_network builds subnet, ip range, LB base and ranges from the index and the octets" {
  local expected=(
    "0 16 0"
    "17 16 34"
    "127 16 254"
    "128 17 0"
    "300 18 88"
    "2047 31 254"
  )
  local row index x y base
  for row in "${expected[@]}"; do
    read -r index x y <<<"${row}"
    base="172.${x}.${y}"
    run_in_fake_repo MCK_DEVC_HOST_INDEX="${index}" MCK_DEVC_NET_X="${x}" MCK_DEVC_NET_Y_BASE="${y}" -- bash -c \
      'source scripts/funcs/kind_network && echo "${KIND_NETWORK_SUBNET} ${KIND_NETWORK_IP_RANGE} ${KIND_LB_PREFIX} ${KIND_DOCKER_NETWORK} ${KIND_NAME_PREFIX} ${KIND_METALLB_RANGE_OPERATOR} ${KIND_METALLB_RANGE_SINGLE}"'
    assert_succeeded
    [ "${output}" = "${base}.0/23 ${base}.0/25 ${base} devc${index} devc${index}- ${base}.200-${base}.209 ${base}.200-${base}.250" ]
  done
}

@test "stack index unset: kind_network keeps network kind, 172.18.0.0/16 and the .255 row" {
  run_in_fake_repo_ok -- bash -c \
    'source scripts/funcs/kind_network && echo "${KIND_NETWORK_SUBNET}|${KIND_NETWORK_IP_RANGE}|${KIND_LB_PREFIX}|${KIND_DOCKER_NETWORK}|${KIND_NAME_PREFIX}|${KIND_METALLB_RANGE_OPERATOR}|${KIND_EXPERIMENTAL_DOCKER_NETWORK:-<unset>}"'
  [ "${output}" = "172.18.0.0/16||172.18.255|kind||172.18.255.200-172.18.255.209|<unset>" ]
}

@test "stack index: a value that is not a number is refused" {
  local bad
  for bad in abc -1 1.5; do
    run_in_fake_repo MCK_DEVC_HOST_INDEX="${bad}" MCK_DEVC_NET_X=16 MCK_DEVC_NET_Y_BASE=34 -- bash -c 'source scripts/funcs/kind_network'
    [ "$status" -ne 0 ]
    [[ "${stderr}" == *"not a stack index"* ]]
  done
}

@test "stack index: kind_network without the address octets is refused, naming them" {
  run_in_fake_repo MCK_DEVC_HOST_INDEX=17 -- bash -c 'source scripts/funcs/kind_network'

  [ "$status" -ne 0 ]
  [[ "${stderr}" == *"needs MCK_DEVC_NET_X and MCK_DEVC_NET_Y_BASE"* ]]
}

@test "kind_logical_kubeconfig: renames cluster, context, user and current-context and leaves the data lines alone" {
  local file="${BATS_TEST_TMPDIR}/kc.yaml"
  kubeconfig_of() {
    for name in "$@"; do
      printf -- '- cluster:\n    certificate-authority-data: LS0tQ0FEQVRB\n    server: https://127.0.0.1:35765\n  name: kind-%s\n' "${name}"
    done
  }
  { echo "apiVersion: v1"; echo "clusters:"; kubeconfig_of devc17-e2e-operator devc17-kind
    printf -- '- context:\n    cluster: kind-devc17-kind\n    user: kind-devc17-kind\n  name: kind-devc17-kind\n'
    echo "current-context: kind-devc17-kind"; echo "kind: Config"
    printf -- 'users:\n- name: kind-devc17-kind\n  user:\n    client-key-data: LS0tS0VZ\n'; } >"${file}"
  grep 'data:' "${file}" >"${BATS_TEST_TMPDIR}/data-before"

  run_in_fake_repo_ok "${STACK_17[@]}" -- bash -c 'source scripts/funcs/kubernetes && kind_logical_kubeconfig "$1"' _ "${file}"
  refute_in_file devc17 "${file}"
  grep -qx 'current-context: kind-kind' "${file}"
  grep -qx '  name: kind-e2e-operator' "${file}"
  grep -qx '    cluster: kind-kind' "${file}"
  grep -qx -- '- name: kind-kind' "${file}"
  grep 'data:' "${file}" | cmp - "${BATS_TEST_TMPDIR}/data-before"
}

@test "kind_logical_kubeconfig: without an index the file is not touched" {
  local file="${BATS_TEST_TMPDIR}/kc.yaml"
  printf 'current-context: kind-devc17-kind\n' >"${file}"

  run_in_fake_repo_ok -- bash -c 'source scripts/funcs/kubernetes && kind_logical_kubeconfig "$1"' _ "${file}"
  [ "$(cat "${file}")" = "current-context: kind-devc17-kind" ]
}

@test "stack index 17: a kubeconfig of an earlier run is dropped, each logical context appears once and interconnect gets the logical names" {
  use_real scripts/dev/interconnect_kind_clusters.sh
  # What the earlier run left in the stack's file, already renamed.
  printf 'clusters:\n- cluster:\n    server: https://127.0.0.1:1\n  name: kind-e2e-operator\ncontexts:\n- context:\n    cluster: kind-e2e-operator\n    user: kind-e2e-operator\n  name: kind-e2e-operator\nusers:\n- name: kind-e2e-operator\n  user: {}\n' \
    >"${FAKE_REPO}/.generated/current.kubeconfig"

  run_in_fake_repo_ok MCK_DEVC_LOCAL_KIND=1 "${STACK_17[@]}" -- bash scripts/dev/recreate_kind_clusters.sh
  local kubeconfig="${FAKE_REPO}/.generated/current.kubeconfig"
  refute_in_file devc17 "${kubeconfig}"
  for name in "${STACK_CLUSTERS[@]}"; do
    [ "$(count_entries "kind-${name}" "${kubeconfig}")" -eq 3 ] # cluster, context and user entry
  done
  grep -qx 'current-context: kind-kind' "${kubeconfig}"
  grep -q '^kubectl --context kind-e2e-cluster-1 wait nodes' "${CALL_LOG}"
  grep -qx 'kind get nodes --name devc17-e2e-cluster-1' "${CALL_LOG}"
  grep -qx 'kind get nodes --name devc17-e2e-operator' "${CALL_LOG}"
  grep -qx 'csi kind-e2e-operator' "${CALL_LOG}"
}

@test "stack index 17: install_istio looks the three kind nodes up under their devc17- names" {
  use_real multi_cluster/tools/install_istio.sh

  run_in_fake_repo_ok "${STACK_17[@]}" VERSION=1.16.1 CTX_CLUSTER1=kind-e2e-cluster-1 CTX_CLUSTER2=kind-e2e-cluster-2 \
    CTX_CLUSTER3=kind-e2e-cluster-3 -- bash multi_cluster/tools/install_istio.sh
  for i in 1 2 3; do
    grep -q "^kubectl --context=kind-e2e-cluster-${i} get node devc17-e2e-cluster-${i}-control-plane " "${CALL_LOG}"
  done
  [ "$(calls_matching 'get node e2e-cluster')" -eq 0 ]
}

@test "stack index 17: single-cluster recreate in scoped mode uses devc17-kind, deletes no network and keeps only kind-kind" {
  run_in_fake_repo_ok MCK_DEVC_LOCAL_KIND=1 "${STACK_17[@]}" -- bash scripts/dev/recreate_kind_cluster.sh kind
  [ "$(calls_matching '^docker stop')" -eq 0 ]
  [ "$(calls_matching '^docker network rm')" -eq 0 ]
  [ "$(calls_matching '^scripts/dev/setup_kind_cluster.sh -r -e -n devc17-kind ')" -eq 1 ]
  grep -qx 'csi kind-devc17-kind' "${CALL_LOG}"
  grep -qx 'current-context: kind-kind' "${FAKE_REPO}/.generated/current.kubeconfig"
  refute_in_file devc17 "${FAKE_REPO}/.generated/current.kubeconfig"
  [ "$(sort -u "${ENV_LOG}")" = "setup_kind_cluster.sh KIND_EXPERIMENTAL_DOCKER_NETWORK=devc17" ]
}

@test "stack index 17: both recreate scripts refuse to run unscoped, before any docker or kind call" {
  run_in_fake_repo "${STACK_17[@]}" -- bash scripts/dev/recreate_kind_clusters.sh
  [ "$status" -ne 0 ]
  [[ "${stderr}" == *"needs MCK_DEVC_LOCAL_KIND=1"* ]]

  run_in_fake_repo "${STACK_17[@]}" -- bash scripts/dev/recreate_kind_cluster.sh kind
  [ "$status" -ne 0 ]
  [[ "${stderr}" == *"needs MCK_DEVC_LOCAL_KIND=1"* ]]

  [ ! -s "${CALL_LOG}" ]
}
