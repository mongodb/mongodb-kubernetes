# The namespace must exist in every cluster: the central cluster (operator + Ops Managers)
# and both AppDB member clusters (where the AppDB data pods run). The istio-injection label is
# required so the AppDB pods get an Istio sidecar and can resolve the central-cluster management
# Ops Manager service across clusters over the mesh — without it their agents fail with
# "Could not resolve host" when downloading the automation agent from the OM baseUrl.
for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}" "${K8S_CTX_2}"; do
  kubectl create namespace "${MDB_NS}" --context "${ctx}" --dry-run=client -o yaml \
    | kubectl apply --context "${ctx}" -f -
  kubectl label namespace "${MDB_NS}" istio-injection=enabled --overwrite --context "${ctx}"
done

echo "[ok] Namespace '${MDB_NS}' ready (istio-injection enabled) in the central and both member clusters"
