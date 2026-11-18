# Reverse migration (graceful): remove spec.externalApplicationDatabaseRef and add back
# spec.applicationDatabase (topology: MultiCluster, same member layout and TLS as the external AppDB).
# The operator asks the MongoDBMultiCluster CR to release the StatefulSets, then re-adopts them as an
# internally-managed multi-cluster AppDB. The CR reports Pending ("other owner") and can be deleted
# once the handover completes. The connection string is unchanged (same hosts).
kubectl patch om "${PRIMARY_OM_NAME}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  --type merge \
  -p "$(cat <<EOF
{
  "spec": {
    "externalApplicationDatabaseRef": null,
    "applicationDatabase": {
      "topology": "MultiCluster",
      "version": "${APPDB_VERSION}",
      "security": {
        "certsSecretPrefix": "${APPDB_CERT_PREFIX}",
        "tls": { "ca": "${APPDB_CA_CONFIGMAP}" }
      },
      "clusterSpecList": [
        { "clusterName": "${K8S_CTX_1}", "members": ${APPDB_MEMBERS_C1} },
        { "clusterName": "${K8S_CTX_2}", "members": ${APPDB_MEMBERS_C2} }
      ]
    }
  }
}
EOF
)"

echo "[ok] Primary OM '${PRIMARY_OM_NAME}' reconfigured to an internal multi-cluster AppDB"
