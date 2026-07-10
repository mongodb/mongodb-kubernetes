MONGOD_TAG="${APPDB_VERSION%%-ent}-ubi8"
MONGOD_IMAGE="quay.io/mongodb/mongodb-enterprise-server:${MONGOD_TAG}"

for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  kubectl --context "${ctx}" -n "${OM_NAMESPACE}" delete daemonset pre-pull-appdb --ignore-not-found
  kubectl --context "${ctx}" -n "${OM_NAMESPACE}" apply -f - <<EOF
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: pre-pull-appdb
spec:
  selector:
    matchLabels:
      app: pre-pull-appdb
  template:
    metadata:
      labels:
        app: pre-pull-appdb
    spec:
      containers:
      - name: pre-pull
        image: "${MONGOD_IMAGE}"
        command: ["sleep", "3600"]
        resources:
          requests:
            memory: "16M"
          limits:
            memory: "16M"
      tolerations:
      - operator: Exists
EOF
done

for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  kubectl --context "${ctx}" -n "${OM_NAMESPACE}" rollout status daemonset pre-pull-appdb --timeout=600s
done

for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  kubectl --context "${ctx}" -n "${OM_NAMESPACE}" delete daemonset pre-pull-appdb
done
