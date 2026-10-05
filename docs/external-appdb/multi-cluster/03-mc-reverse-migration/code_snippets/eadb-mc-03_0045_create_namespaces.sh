# The namespace must exist in every cluster: the central cluster (operator + Ops Managers)
# and both AppDB member clusters (where the AppDB data pods run).
for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}" "${K8S_CTX_2}"; do
  kubectl create namespace "${MDB_NS}" --context "${ctx}" --dry-run=client -o yaml \
    | kubectl apply --context "${ctx}" -f -
done

echo "[ok] Namespace '${MDB_NS}' ready in the central and both member clusters"
