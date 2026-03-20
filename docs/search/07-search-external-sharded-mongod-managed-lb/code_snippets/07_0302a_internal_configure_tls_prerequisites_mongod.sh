echo "Distributing CA certificate (ConfigMap) for mongod..."

ca_tmp=$(mktemp)
trap 'rm -f "${ca_tmp}"' EXIT

kubectl get secret "${MDB_TLS_CA_SECRET_NAME}" \
  -n "${CERT_MANAGER_NAMESPACE}" \
  --context "${K8S_CTX}" \
  -o jsonpath='{.data.tls\.crt}' | base64 -d > "${ca_tmp}"

kubectl create configmap "${MDB_TLS_CA_CONFIGMAP}" \
  --from-file=ca-pem="${ca_tmp}" \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

echo "✓ CA ConfigMap distributed for mongod"
