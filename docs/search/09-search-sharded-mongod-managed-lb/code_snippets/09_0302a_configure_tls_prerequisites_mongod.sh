#!/usr/bin/env bash
# Configure CA certificate for mongod: create ConfigMap with CA in target namespace
#
# MongoDB Enterprise expects the CA in a ConfigMap (key "ca-pem").

echo "Configuring CA certificate (ConfigMap) for mongod..."

mkdir -p certs

kubectl get secret "${MDB_TLS_CA_SECRET_NAME}" \
  -n "${CERT_MANAGER_NAMESPACE}" \
  --context "${K8S_CTX}" \
  -o jsonpath='{.data.tls\.crt}' | base64 -d > certs/ca-pem

kubectl create configmap "${MDB_TLS_CA_CONFIGMAP}" \
  --from-file=ca-pem=certs/ca-pem \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

echo "✓ CA ConfigMap configured for mongod"
