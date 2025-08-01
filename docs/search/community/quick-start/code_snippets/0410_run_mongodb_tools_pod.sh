#!/bin/bash

kubectl apply -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: mongodb-tools-pod
  labels:
    app: mongodb-tools
spec:
  containers:
  - name: mongodb-tools
    image: mongodb/mongodb-community-server:${MDB_VERSION}-ubi8
    command: ["/bin/bash", "-c"]
    args: ["sleep infinity"]
  restartPolicy: Never
EOF

echo "Waiting for the mongodb-tools to be ready..."
kubectl wait --for=condition=Ready pod/mongodb-tools-pod -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --timeout=60s
