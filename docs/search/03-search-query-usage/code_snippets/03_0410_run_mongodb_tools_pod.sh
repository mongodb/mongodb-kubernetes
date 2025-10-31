kubectl apply -n "${MDB_NS}" --context "${K8S_CTX}" -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: mongodb-tools-pod
  labels:
    app: mongodb-tools
spec:
  containers:
  - name: mongodb-tools
    image: mongodb/mongodb-community-server:${MDB_VERSION%-ent}-ubi8
    command: ["/bin/bash", "-c"]
    args: ["sleep infinity"]
    volumeMounts:
    - name: mongo-ca
      mountPath: /tls
      readOnly: true
  restartPolicy: Never
  volumes:
  - name: mongo-ca
    secret:
      secretName: ${MDB_TLS_CA_SECRET_NAME}
EOF

echo "Waiting for the mongodb-tools to be ready..."
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready pod/mongodb-tools-pod --timeout=60s
