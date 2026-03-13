#!/usr/bin/env bash
# Deploy a mongodb-tools pod for running database commands
#
# This pod provides mongosh, mongorestore, and other MongoDB tools
# for importing data and running queries against the cluster.

echo "Deploying mongodb-tools pod..."

# Get the CA certificate for TLS connections
kubectl get configmap "${MDB_TLS_CA_CONFIGMAP}" -n "${MDB_NS}" --context "${K8S_CTX}" \
  -o jsonpath='{.data.ca-pem}' > /tmp/ca.crt

kubectl run mongodb-tools \
  --image=mongodb/mongodb-community-server:8.0-ubi9 \
  --restart=Never \
  --namespace="${MDB_NS}" \
  --context "${K8S_CTX}" \
  --overrides='{
    "spec": {
      "containers": [{
        "name": "mongodb-tools",
        "image": "mongodb/mongodb-community-server:8.0-ubi9",
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

