echo "Creating the CA ConfigMap (used by the source MongoDB's own TLS and by MongoDBSearch)..."

# One ConfigMap, two consumers with different key expectations:
#   - the source MongoDB's security.tls.ca reads key "ca-pem"
#   - MongoDBSearch's spec.source.external.tls.ca reads key "ca.crt"
#     (CreateVolumeFromConfigMap in sharded_external_search_source.go --
#     this is a ConfigMap, never a Secret, despite what older docs in this
#     repo say)
# Writing both keys into one ConfigMap avoids maintaining two CA objects.
# Cluster 0 needs it for the source; cluster 1 needs it because its Search
# operator's secrets-presence check expects tls.ca cluster-invariantly.
ca_pem=$(kubectl get secret "${MDB_TLS_CA_SECRET_NAME}" \
  -n "${CERT_MANAGER_NAMESPACE}" \
  --context "${K8S_CLUSTER_0_CONTEXT_NAME}" \
  -o jsonpath='{.data.tls\.crt}' | base64 -d)

for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}"; do
  kubectl create configmap "${MDBS_TLS_CA_CONFIGMAP}" \
    --from-literal=ca-pem="${ca_pem}" \
    --from-literal=mms-ca.crt="${ca_pem}" \
    --from-literal=ca.crt="${ca_pem}" \
    -n "${MDB_NAMESPACE}" \
    --context "${ctx}" \
    --dry-run=client -o yaml | kubectl apply --context "${ctx}" -f -

  echo "[ok] CA ConfigMap '${MDBS_TLS_CA_CONFIGMAP}' present in cluster ${ctx}"
done
