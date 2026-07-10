#!/usr/bin/env bash

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

source scripts/code_snippets/sample_test_runner.sh

pushd "${script_dir}"

prepare_snippets

run ra-01_9010_delete_gke_clusters.sh

popd

# GKE auto-deletes its own managed firewall rules (gke-<cluster>-<hash>-*) when a
# cluster is deleted, but the LoadBalancer Service firewall rules (k8s-fw-*,
# k8s-*-hc) are left behind. Delete this run's leftovers so they don't pile up
# and exhaust the project FIREWALLS quota across CI runs. The LB rules carry the
# node network tag gke-<cluster>-<hash>-node, so match on that. Best-effort.
if [[ -n "${MDB_GKE_PROJECT:-}" ]]; then
  for cluster in "${K8S_CLUSTER_0:-}" "${K8S_CLUSTER_1:-}" "${K8S_CLUSTER_2:-}"; do
    [[ -z "${cluster}" ]] && continue
    leaked=$(gcloud compute firewall-rules list \
      --project="${MDB_GKE_PROJECT}" \
      --filter="targetTags.list()~gke-${cluster}-" \
      --format="value(name)" || true)
    if [[ -n "${leaked}" ]]; then
      # shellcheck disable=SC2086 # intentional word-split: pass each rule as an arg
      gcloud compute firewall-rules delete ${leaked} \
        --project="${MDB_GKE_PROJECT}" --quiet || true
    fi
  done
fi
