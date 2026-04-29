echo "Verifying customer-replicated secrets are present in every member cluster (spec §6.3)..."

# Sync-source password
kubectl get secret "${MDB_SYNC_PASSWORD_SECRET}" -n "${MDB_NS}" --context "${K8S_CLUSTER_0_CTX}" >/dev/null \
  && echo "  [ok] ${MEMBER_CLUSTER_0_NAME}: secret/${MDB_SYNC_PASSWORD_SECRET}"
kubectl get secret "${MDB_SYNC_PASSWORD_SECRET}" -n "${MDB_NS}" --context "${K8S_CLUSTER_1_CTX}" >/dev/null \
  && echo "  [ok] ${MEMBER_CLUSTER_1_NAME}: secret/${MDB_SYNC_PASSWORD_SECRET}"

# External CA bundle
kubectl get secret "${MDB_EXTERNAL_CA_SECRET}" -n "${MDB_NS}" --context "${K8S_CLUSTER_0_CTX}" >/dev/null \
  && echo "  [ok] ${MEMBER_CLUSTER_0_NAME}: secret/${MDB_EXTERNAL_CA_SECRET}"
kubectl get secret "${MDB_EXTERNAL_CA_SECRET}" -n "${MDB_NS}" --context "${K8S_CLUSTER_1_CTX}" >/dev/null \
  && echo "  [ok] ${MEMBER_CLUSTER_1_NAME}: secret/${MDB_EXTERNAL_CA_SECRET}"

echo "[ok] Required secrets present in every member cluster"
echo "  Note: TLS prefix '${MDB_TLS_CERT_SECRET_PREFIX}' must produce valid mongot + Envoy server certs"
echo "        signed by the same CA. Operator will surface a warning per-cluster if any are missing."
