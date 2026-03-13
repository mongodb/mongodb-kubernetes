#!/usr/bin/env bash
# Deploy a mongodb-tools pod for running database commands
#
# This pod provides mongosh, mongorestore, and other MongoDB tools
# for importing data and running queries against the cluster.

echo "Deploying mongodb-tools pod..."

# Get the CA certificate for TLS connections
kubectl get configmap "${MDB_TLS_CA_CONFIGMAP}" -n "${MDB_NS}" --context "${K8S_CTX}" \
  -o jsonpath='{.data.ca-pem}' > /tmp/ca.crt

# Use the same MongoDB version as the cluster (strip -ent suffix for community image)
TOOLS_IMAGE="mongodb/mongodb-community-server:${MDB_VERSION%-ent}-ubi8"

kubectl run mongodb-tools \
  --image="${TOOLS_IMAGE}" \
  --restart=Never \
  --namespace="${MDB_NS}" \
  --context "${K8S_CTX}" \
  --overrides='{
    "spec": {
      "containers": [{
        "name": "mongodb-tools",
        "image": "'"${TOOLS_IMAGE}"'",
        "command": ["sleep", "infinity"],
        "volumeMounts": [{
          "name": "tls",
          "mountPath": "/tls",
          "readOnly": true
        }]
      }],
      "volumes": [{
        "name": "tls",
        "configMap": {
          "name": "'"${MDB_TLS_CA_CONFIGMAP}"'"
        }
      }]
    }
  }' \
  --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

rm -f /tmp/ca.crt

# Wait for pod to be ready
echo "Waiting for mongodb-tools pod to be ready..."
kubectl wait --for=condition=Ready pod/mongodb-tools \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --timeout=120s

echo "✓ mongodb-tools pod is ready"
echo ""
echo "You can now run MongoDB commands using:"
echo "  kubectl exec -it mongodb-tools -n ${MDB_NS} -- mongosh \"${MDB_CONNECTION_STRING}\""
