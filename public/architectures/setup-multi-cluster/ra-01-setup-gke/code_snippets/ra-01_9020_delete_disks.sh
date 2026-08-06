# Delete persistent disks owned by these clusters.
# Disks may remain after cluster deletion (PVCs with reclaimPolicy: Retain).
# GKE persists the owning cluster name in labels.goog-k8s-cluster-name; zone
# alone is not an ownership boundary because CI runs share the project.

delete_disks() {
  local cluster="${1}" zone="${2}" disks

  if [[ -z "${cluster}" ]] || ! [[ "${cluster}" =~ ^k8s-mdb-[0-2](-[a-z0-9]+)*$ ]]; then
    echo "Skipping disk cleanup in ${zone}: invalid or missing cluster owner"
    return
  fi

  echo "Checking for disks in zone: ${zone}"
  disks=$(gcloud compute disks list \
    --project="${MDB_GKE_PROJECT}" \
    --filter="zone:${zone} AND name~^pvc-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ AND labels.goog-k8s-cluster-name=${cluster} AND description~storage.gke.io/created-by AND description~pd.csi.storage.gke.io" \
    --format="csv[no-heading](name)" 2>/dev/null || echo "")

  if [[ -z "${disks}" ]]; then
    echo "No disks found in ${zone}"
    return
  fi

  while IFS= read -r disk_name; do
    [[ -z "${disk_name}" ]] && continue
    echo "Deleting disk: ${disk_name} in ${zone}"
    gcloud compute disks delete "${disk_name}" \
      --zone="${zone}" \
      --project="${MDB_GKE_PROJECT}" \
      --quiet || echo "Warning: Failed to delete disk ${disk_name}"
  done <<< "${disks}"
}

delete_disks "${K8S_CLUSTER_0:-}" "${K8S_CLUSTER_0_ZONE}"
delete_disks "${K8S_CLUSTER_1:-}" "${K8S_CLUSTER_1_ZONE}"
delete_disks "${K8S_CLUSTER_2:-}" "${K8S_CLUSTER_2_ZONE}"
