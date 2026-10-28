#!/usr/bin/env bats
# The commands the kind scripts issue. The harness (recreate_harness.bash) says what is real
# and what is a stub.
#
# Golden tests: the sorted argv log of each scenario must equal fixtures/argv-<scenario>.log.
# CI and every local kind setup depend on those commands. Sorting hides the order of the five
# parallel cluster creations only. Record the fixtures again with GOLDEN_RECORD=1 on a tree
# that is known good.

load recreate_harness

setup() {
  recreate_setup
}

# golden_run <fixture> <VAR=value>... -- <command>...
golden_run() {
  local fixture="$1"
  shift
  run_in_fake_repo_ok "$@"
  assert_golden "${fixture}"
}

@test "golden: recreate_kind_clusters issues the commands of the multi-cluster setup" {
  golden_run recreate-clusters -- bash scripts/dev/recreate_kind_clusters.sh
}

@test "golden: recreate_kind_cluster kind issues the commands of the single-cluster setup" {
  golden_run recreate-cluster -- bash scripts/dev/recreate_kind_cluster.sh kind
}

@test "golden: docker_create_kind_network creates the kind network" {
  golden_run create-network -- bash -c 'source scripts/funcs/kubernetes && docker_create_kind_network'
}

@test "golden: docker_run_local_registry starts the registry" {
  golden_run run-registry -- bash -c 'source scripts/funcs/kubernetes && docker_run_local_registry kind-registry 5000'
}

@test "kind_network defaults: subnet 172.18.0.0/16 and the .255 row" {
  run_in_fake_repo_ok -- bash -c \
    'source scripts/funcs/kind_network && echo "${KIND_NETWORK_SUBNET}|${KIND_NETWORK_PREFIX}|${KIND_LB_PREFIX}|${KIND_METALLB_RANGE_SINGLE}"'
  [ "${output}" = "172.18.0.0/16|172.18|172.18.255|172.18.255.200-172.18.255.250" ]
}

@test "kind_network: KIND_NETWORK_SUBNET moves the prefix and the ranges, KIND_LB_PREFIX moves the ranges only" {
  run_in_fake_repo_ok KIND_NETWORK_SUBNET=10.9.0.0/16 -- bash -c \
    'source scripts/funcs/kind_network && echo "${KIND_NETWORK_PREFIX}|${KIND_LB_PREFIX}|${KIND_METALLB_RANGE_SINGLE}"'
  [ "${output}" = "10.9|10.9.255|10.9.255.200-10.9.255.250" ]

  run_in_fake_repo_ok KIND_LB_PREFIX=172.16.34 -- bash -c \
    'source scripts/funcs/kind_network && echo "${KIND_NETWORK_PREFIX}|${KIND_LB_PREFIX}|${KIND_METALLB_RANGE_SINGLE}"'
  [ "${output}" = "172.18|172.16.34|172.16.34.200-172.16.34.250" ]
}

@test "docker_create_kind_network: a missing network is created with KIND_NETWORK_SUBNET" {
  run_in_fake_repo_ok KIND_NETWORK_SUBNET=10.9.0.0/16 -- bash -c 'source scripts/funcs/kubernetes && docker_create_kind_network'
  grep -qx 'docker network create --subnet=10.9.0.0/16 kind' "${CALL_LOG}"
}

@test "docker_create_kind_network: an existing kind network with the configured subnet is reused" {
  run_in_fake_repo_ok DOCKER_STUB_NET_SUBNET="172.18.0.0/16 " -- bash -c 'source scripts/funcs/kubernetes && docker_create_kind_network'
  [ "$(calls_matching '^docker network create')" -eq 0 ]
}

@test "docker_create_kind_network: an existing dual-stack kind network is reused" {
  # kind creates its network with an IPv6 subnet too when it creates the network itself.
  run_in_fake_repo_ok DOCKER_STUB_NET_SUBNET="172.18.0.0/16 fc00:f853:ccd:e793::/64 " -- bash -c 'source scripts/funcs/kubernetes && docker_create_kind_network'
  [ "$(calls_matching '^docker network create')" -eq 0 ]
}

@test "docker_create_kind_network: an existing kind network with another subnet is an error that names it" {
  run_in_fake_repo DOCKER_STUB_NET_SUBNET="172.19.0.0/16 " -- bash -c 'source scripts/funcs/kubernetes && docker_create_kind_network'

  [ "$status" -ne 0 ]
  [[ "${stderr}" == *"docker network 'kind' exists with subnet(s) '172.19.0.0/16' but KIND_NETWORK_SUBNET=172.18.0.0/16"* ]]
  [[ "${stderr}" == *"docker network rm kind"* ]]
  [ "$(calls_matching '^docker network create')" -eq 0 ]
}

@test "recreate_kind_cluster: the CSI driver goes to the kind-<name> context of the new cluster" {
  run_in_fake_repo_ok -- bash scripts/dev/recreate_kind_cluster.sh foo

  grep -q -- 'setup_kind_cluster.sh .* -n foo ' "${CALL_LOG}"
  grep -qx 'csi kind-foo' "${CALL_LOG}"
}
