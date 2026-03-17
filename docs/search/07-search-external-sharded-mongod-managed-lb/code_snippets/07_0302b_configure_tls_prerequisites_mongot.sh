#!/usr/bin/env bash
# Distribute CA certificate for mongot: create Secret with CA in target namespace
#
# MongoDBSearch expects the CA in a Secret (key "ca.crt").

echo "Distributing CA certificate (Secret) for mongot..."

ca_tmp=$(mktemp)
trap 'rm -f "${ca_tmp}"' EXIT

kubectl get secret "${MDB_TLS_CA_SECRET_NAME}" \
  -n "${CERT_MANAGER_NAMESPACE}" \
  --context "${K8S_CTX}" \
  -o jsonpath='{.data.tls\.crt}' | base64 -d > "${ca_tmp}"

kubectl create secret generic "${MDB_TLS_CA_SECRET_NAME}" \
  --from-file=ca.crt="${ca_tmp}" \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

echo "✓ CA Secret distributed for mongot"
