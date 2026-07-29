delete_failed=0

yes | gcloud container clusters delete "${K8S_CLUSTER_0}" --zone="${K8S_CLUSTER_0_ZONE}" &
pid0=$!
yes | gcloud container clusters delete "${K8S_CLUSTER_1}" --zone="${K8S_CLUSTER_1_ZONE}" &
pid1=$!
yes | gcloud container clusters delete "${K8S_CLUSTER_2}" --zone="${K8S_CLUSTER_2_ZONE}" &
pid2=$!

wait "${pid0}" || delete_failed=1
wait "${pid1}" || delete_failed=1
wait "${pid2}" || delete_failed=1

if [[ "${delete_failed}" != "0" ]]; then
  echo "WARNING: failed to delete one or more GKE clusters (see errors above). Clusters still present:"
  gcloud container clusters list || true
fi

exit "${delete_failed}"
