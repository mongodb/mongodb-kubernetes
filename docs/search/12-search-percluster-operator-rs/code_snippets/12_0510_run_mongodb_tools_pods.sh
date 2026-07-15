echo "Starting a mongodb-tools pod in EVERY member cluster..."
echo "One pod per cluster lets 12_0540 query each cluster's LOCAL replica-set member,"
echo "proving that cluster's own mongot serves search results."

for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  kubectl apply -n "${MDB_NAMESPACE}" --context "${ctx}" -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: mongodb-tools-pod
  labels:
    app: mongodb-tools
spec:
  containers:
  - name: mongodb-tools
    image: mongodb/mongodb-community-server:8.2.4-ubi9
    command: ["/bin/bash", "-c"]
    args: ["sleep infinity"]
    volumeMounts:
    - name: mongo-ca
      mountPath: /tls
      readOnly: true
  restartPolicy: Never
  volumes:
  - name: mongo-ca
    configMap:
      # Reuses the source CA ConfigMap 12_0303 already replicated to every cluster.
      name: ${SOURCE_CA_CONFIGMAP}
      items:
      - key: ca.crt
        path: ca.crt
EOF
done

for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  kubectl --context "${ctx}" -n "${MDB_NAMESPACE}" wait --for=condition=Ready pod/mongodb-tools-pod --timeout=300s
  echo "  [ok] mongodb-tools-pod ready in ${ctx}"
done
