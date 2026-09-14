# Connection ConfigMap the AppDB CR uses to register itself as a project on the management OM.
# baseUrl points at the in-cluster management OM service; credentials come from the operator-provisioned
# admin-key secret (referenced later by the MongoDB CR's spec.credentials).
kubectl create configmap "${APPDB_PROJECT_CONFIGMAP}" \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  --from-literal=baseUrl="${MANAGEMENT_OM_URL}" \
  --from-literal=projectName="${APPDB_PROJECT_NAME}" \
  --from-literal=orgId="" \
  --dry-run=client -o yaml \
  | kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f -

echo "[ok] Project ConfigMap '${APPDB_PROJECT_CONFIGMAP}' ready (baseUrl=${MANAGEMENT_OM_URL})"
