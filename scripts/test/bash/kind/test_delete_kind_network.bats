#!/usr/bin/env bats
# delete_kind_network: which containers it stops and removes before it deletes the kind
# network. The harness (recreate_harness.bash) says what is real and what is a stub.

load recreate_harness

setup() {
  recreate_setup
}

@test "delete_kind_network: removes the stopped containers by name and never prunes" {
  run_in_fake_repo_ok DOCKER_STUB_NET_CONTAINERS="a b " -- bash -c \
    'source scripts/funcs/kubernetes && delete_kind_network'
  grep -qx 'docker stop a b' "${CALL_LOG}"
  grep -qx 'docker rm -f a b' "${CALL_LOG}"
  grep -qx 'docker network rm kind' "${CALL_LOG}"
  [ "$(calls_matching 'prune')" -eq 0 ]
}

@test "delete_kind_network: a kind network without containers is only removed" {
  run_in_fake_repo_ok -- bash -c 'source scripts/funcs/kubernetes && delete_kind_network'
  [ "$(calls_matching '^docker stop')" -eq 0 ]
  [ "$(calls_matching '^docker rm')" -eq 0 ]
  grep -qx 'docker network rm kind' "${CALL_LOG}"
}
