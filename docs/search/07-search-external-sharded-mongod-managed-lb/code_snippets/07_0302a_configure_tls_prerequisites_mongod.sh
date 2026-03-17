#!/usr/bin/env bash
# AUDIENCE: internal
# Distribute CA certificate for mongod: create ConfigMap with CA in target namespace
#
# MongoDB Enterprise expects the CA in a ConfigMap (key "ca-pem").
# This is only needed for the simulated external mongod cluster running in K8s.
# In production, the external mongod cluster manages its own CA distribution.

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
