#!/usr/bin/env bats
# Scoped mode of the kind recreate scripts (MCK_DEVC_LOCAL_KIND=1): which clusters and
# networks they delete and which kubeconfig they write. The commands of the default mode
# are pinned in test_kind_scripts.bats; the harness (recreate_harness.bash) says what is
# real and what is a stub.
#
# Golden tests: the sorted argv log of each scoped scenario must equal
# fixtures/argv-<scenario>.log. Record the fixtures again with GOLDEN_RECORD=1 on a tree
# that is known good.

load recreate_harness

setup() {
  recreate_setup
}

SCOPED_CLUSTERS=(e2e-operator e2e-cluster-1 e2e-cluster-2 e2e-cluster-3 kind)

@test "golden: scoped recreate_kind_clusters issues the commands of the multi-cluster setup without the global cleanup" {
  run_in_fake_repo_ok MCK_DEVC_LOCAL_KIND=1 -- bash scripts/dev/recreate_kind_clusters.sh
  assert_golden recreate-clusters-scoped
}

@test "golden: scoped recreate_kind_cluster kind issues the commands of the single-cluster setup without deleting the network" {
  run_in_fake_repo_ok MCK_DEVC_LOCAL_KIND=1 -- bash scripts/dev/recreate_kind_cluster.sh kind
  assert_golden recreate-cluster-scoped
}

@test "scoped recreate_kind_clusters deletes exactly its five clusters: no --all, no prune, no network" {
  run_in_fake_repo_ok MCK_DEVC_LOCAL_KIND=1 -- bash scripts/dev/recreate_kind_clusters.sh

  [ "$(calls_matching '^kind delete cluster --name ')" -eq 5 ]
  local name
  for name in "${SCOPED_CLUSTERS[@]}"; do
    [ "$(calls_matching "^kind delete cluster --name ${name}\$")" -eq 1 ]
  done
  [ "$(calls_matching 'kind delete clusters --all')" -eq 0 ]
  [ "$(calls_matching 'prune')" -eq 0 ]
  [ "$(calls_matching '^docker network rm')" -eq 0 ]
}

@test "scoped recreate_kind_cluster recreates only its cluster and keeps the network although the context asks to delete it" {
  run_in_fake_repo_ok MCK_DEVC_LOCAL_KIND=1 -- bash scripts/dev/recreate_kind_cluster.sh foo

  [ "$(calls_matching '^scripts/dev/setup_kind_cluster.sh -r -e -n foo ')" -eq 1 ] # -r recreates only foo
  [ "$(calls_matching '^docker stop')" -eq 0 ]
  [ "$(calls_matching '^docker network rm')" -eq 0 ]
}

@test "scoped recreate_kind_cluster: every helper uses the kubeconfig of the checkout" {
  run_in_fake_repo_ok MCK_DEVC_LOCAL_KIND=1 -- bash scripts/dev/recreate_kind_cluster.sh foo

  [ "$(grep -c . "${KUBECONFIG_LOG}")" -ge 2 ] # setup_kind_cluster.sh and the CSI deploy
  [ "$(sort -u "${KUBECONFIG_LOG}")" = "${FAKE_REPO}/.generated/current.kubeconfig" ]
}

@test "scoped recreate_kind_clusters: every helper and every kind export use the kubeconfig of the checkout" {
  run_in_fake_repo_ok MCK_DEVC_LOCAL_KIND=1 -- bash scripts/dev/recreate_kind_clusters.sh

  [ "$(grep -c . "${KUBECONFIG_LOG}")" -ge 10 ]
  [ "$(sort -u "${KUBECONFIG_LOG}")" = "${FAKE_REPO}/.generated/current.kubeconfig" ]
}

@test "unscoped recreate scripts leave the KUBECONFIG of the context alone" {
  run_in_fake_repo_ok -- bash scripts/dev/recreate_kind_cluster.sh foo
  [ "$(sort -u "${KUBECONFIG_LOG}")" = "${FAKE_HOME}/.kube/config" ]

  : >"${KUBECONFIG_LOG}"
  run_in_fake_repo_ok -- bash scripts/dev/recreate_kind_clusters.sh
  [ "$(sort -u "${KUBECONFIG_LOG}")" = "${FAKE_HOME}/.kube/config" ]
}
