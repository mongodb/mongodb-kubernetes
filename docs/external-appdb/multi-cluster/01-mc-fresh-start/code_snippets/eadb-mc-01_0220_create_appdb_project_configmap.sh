# Connection ConfigMap the AppDB CR uses to register itself as a project on the management OM.
# baseUrl points at the in-cluster management OM service; sslMMSCAConfigMap advertises the CA the
# TLS-enabled AppDB agents must trust when reaching Ops Manager; credentials come from the
# operator-provisioned admin-key secret (referenced by the MongoDBMultiCluster CR's spec.credentials).
kubectl create configmap "${APPDB_PROJECT_CONFIGMAP}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  --from-literal=baseUrl="${MANAGEMENT_OM_URL}" \
  --from-literal=projectName="${APPDB_PROJECT_NAME}" \
  --from-literal=sslMMSCAConfigMap="${APPDB_CA_CONFIGMAP}" \
  --from-literal=orgId="" \
  --dry-run=client -o yaml \
  | kubectl apply --context "${K8S_CTX_0}" -n "${MDB_NS}" -f -

echo "[ok] Project ConfigMap '${APPDB_PROJECT_CONFIGMAP}' ready (baseUrl=${MANAGEMENT_OM_URL})"
