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

@test "docker_run_local_registry: a running registry is left alone" {
  run_in_fake_repo_ok DOCKER_STUB_REGISTRY_RUNNING=true -- \
    bash -c 'source scripts/funcs/kubernetes && docker_run_local_registry kind-registry 5000'

  [ "$(calls_matching '^docker rm')" -eq 0 ]
  [ "$(calls_matching '^docker run')" -eq 0 ]
}

@test "docker_run_local_registry: losing the start race is fine when the registry of the other setup is running" {
  run_in_fake_repo_ok DOCKER_STUB_RUN_FAILS=1 DOCKER_STUB_REGISTRY_RUNNING_AFTER_RUN=true -- \
    bash -c 'source scripts/funcs/kubernetes && docker_run_local_registry kind-registry 5000'
}

@test "docker_run_local_registry: a failed start with no registry running afterwards is an error" {
  run_in_fake_repo DOCKER_STUB_RUN_FAILS=1 -- \
    bash -c 'source scripts/funcs/kubernetes && docker_run_local_registry kind-registry 5000'

  [ "$status" -ne 0 ]
  [[ "${stderr}" == *"could not start the local registry kind-registry"* ]]
}

@test "golden: interconnect_kind_clusters issues the commands of the multi-cluster setup" {
  use_real scripts/dev/interconnect_kind_clusters.sh

  golden_run interconnect -- bash scripts/dev/interconnect_kind_clusters.sh -v \
    e2e-cluster-1 e2e-cluster-2 e2e-cluster-3 e2e-operator
}

@test "golden: install_istio on kind contexts issues the commands of the multi-cluster setup" {
  use_real multi_cluster/tools/install_istio.sh

  golden_run install-istio VERSION=1.16.1 CTX_CLUSTER1=kind-e2e-cluster-1 CTX_CLUSTER2=kind-e2e-cluster-2 \
    CTX_CLUSTER3=kind-e2e-cluster-3 -- bash multi_cluster/tools/install_istio.sh
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

# The node image comes from an ECR mirror. Both recreate scripts log in before kind pulls it.
ECR_NODE_IMAGE="123456789012.dkr.ecr.eu-west-1.amazonaws.com/kind/node:v1"

# first_call_line <pattern>: number of the first line of the call log that matches.
first_call_line() {
  grep -n -- "$1" "${CALL_LOG}" | head -1 | cut -d: -f1
}

@test "recreate_kind_cluster: an uncached ECR node image is logged in to before the cluster is created" {
  run_in_fake_repo_ok KIND_NODE_IMAGE="${ECR_NODE_IMAGE}" -- bash scripts/dev/recreate_kind_cluster.sh kind

  grep -qx 'aws ecr get-login-password --region eu-west-1' "${CALL_LOG}"
  grep -qx 'docker login --username AWS --password-stdin 123456789012.dkr.ecr.eu-west-1.amazonaws.com' "${CALL_LOG}"
  [ "$(first_call_line '^docker login')" -lt "$(first_call_line '^scripts/dev/setup_kind_cluster.sh')" ]
}

@test "recreate_kind_clusters: an uncached ECR node image is logged in to before any cluster is created" {
  run_in_fake_repo_ok KIND_NODE_IMAGE="${ECR_NODE_IMAGE}" -- bash scripts/dev/recreate_kind_clusters.sh

  [ "$(calls_matching '^docker login')" -eq 1 ]
  [ "$(first_call_line '^docker login')" -lt "$(first_call_line '^scripts/dev/setup_kind_cluster.sh')" ]
}
