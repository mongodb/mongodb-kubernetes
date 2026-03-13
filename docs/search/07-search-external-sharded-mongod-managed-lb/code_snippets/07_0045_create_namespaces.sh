#!/usr/bin/env bash
# Create the Kubernetes namespace for MongoDB resources
#
# This namespace will contain:
# - The simulated external MongoDB sharded cluster
# - The MongoDB Search pods (mongot)
# - The operator-managed Envoy proxy
# - TLS secrets and ConfigMaps

kubectl create namespace "${MDB_NS}" --context "${K8S_CTX}" --dry-run=client -o yaml | \
  kubectl apply --context "${K8S_CTX}" -f -

echo "✓ Namespace '${MDB_NS}' ready"

