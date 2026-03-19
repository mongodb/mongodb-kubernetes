#!/usr/bin/env bash
# Configuring CA certificate for mongot: create Secret with CA in target namespace
#
# MongoDBSearch expects the CA in a Secret (key "ca.crt").

echo "Configuring CA certificate (Secret) for mongot..."

mkdir -p certs

kubectl get secret "${MDB_TLS_CA_SECRET_NAME}" \
  -n "${CERT_MANAGER_NAMESPACE}" \
  --context "${K8S_CTX}" \
  -o jsonpath='{.data.tls\.crt}' | base64 -d > certs/ca.crt

kubectl create secret generic "${MDB_TLS_CA_SECRET_NAME}" \
  --from-file=ca.crt=certs/ca.crt \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

echo "✓ CA Secret configured for mongot"
