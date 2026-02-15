# Create ConfigMap for Ops Manager/Cloud Manager project configuration
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: om-project
data:
  projectName: ${OPS_MANAGER_PROJECT_NAME}
  baseUrl: ${OPS_MANAGER_API_URL}
  orgId: ${OPS_MANAGER_ORG_ID}
EOF

# Create Secret for Ops Manager/Cloud Manager credentials
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: om-credentials
type: Opaque
stringData:
  user: ${OPS_MANAGER_API_USER}
  publicApiKey: ${OPS_MANAGER_API_KEY}
EOF

