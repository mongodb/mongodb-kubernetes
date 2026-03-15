#!/usr/bin/env bash
# Deploy a mongodb-tools pod for running database commands
#
# This pod provides mongosh, mongorestore, and other MongoDB tools
# for importing data and running queries against the cluster.
#
# The pod mounts the CA certificate ConfigMap at /tls for TLS connections.

echo "Deploying mongodb-tools pod..."

# Use the same MongoDB version as the cluster (strip -ent suffix for community image)
# ${MDB_VERSION%-ent} removes the "-ent" suffix: "8.2.0-ent" becomes "8.2.0"
TOOLS_IMAGE="mongodb/mongodb-community-server:${MDB_VERSION%-ent}-ubi8"

echo "  Image: ${TOOLS_IMAGE}"
echo "  TLS CA ConfigMap: ${MDB_TLS_CA_CONFIGMAP}"

# Create the pod using a heredoc YAML manifest
# This is clearer than using --overrides with embedded JSON
kubectl apply --context "${K8S_CTX}" -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: mongodb-tools
  namespace: ${MDB_NS}
spec:
  restartPolicy: Never
  containers:
    - name: mongodb-tools
      image: ${TOOLS_IMAGE}
      command: ["sleep", "infinity"]
      volumeMounts:
        - name: tls
          mountPath: /tls
          readOnly: true
  volumes:
    - name: tls
      configMap:
        # CA certificate is mounted at /tls/ca-pem
        name: ${MDB_TLS_CA_CONFIGMAP}
EOF

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
