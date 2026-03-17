#!/usr/bin/env bash
# AUDIENCE: internal
# Create the Kubernetes namespace for MongoDB resources

kubectl create namespace "${MDB_NS}" --context "${K8S_CTX}" --dry-run=client -o yaml | \
  kubectl apply --context "${K8S_CTX}" -f -

echo "✓ Namespace '${MDB_NS}' ready"

