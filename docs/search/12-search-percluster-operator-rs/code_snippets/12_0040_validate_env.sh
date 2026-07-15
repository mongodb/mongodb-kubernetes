echo "Validating environment variables..."

required_vars=(
  "K8S_CLUSTER_0_CONTEXT_NAME"
  "K8S_CLUSTER_1_CONTEXT_NAME"
  "K8S_CLUSTER_2_CONTEXT_NAME"
  "MDB_NAMESPACE"
  "OM_NAMESPACE"
  "OPERATOR_NAMESPACE"
  "OPERATOR_HELM_CHART"
  "RS_RESOURCE_NAME"
  "SEARCH_RESOURCE_NAME"
  "SEARCH_OPERATOR_NAME"
  "SEARCH_MONGOT_REPLICAS"
  "SEARCH_SYNC_USER_NAME"
  "SEARCH_SYNC_USER_PASSWORD"
  "SEARCH_TLS_CERT_SECRET_PREFIX"
  "MDB_TLS_CA_ISSUER"
  "SOURCE_CA_CONFIGMAP"
)

missing_vars=()
for var in "${required_vars[@]}"; do
  [[ -n "${!var:-}" ]] && [[ "${!var}" != "<"* ]] || missing_vars+=("${var}")
done

missing_contexts=()
for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME:-}" "${K8S_CLUSTER_1_CONTEXT_NAME:-}" "${K8S_CLUSTER_2_CONTEXT_NAME:-}"; do
  kubectl config get-contexts "${ctx}" &>/dev/null || missing_contexts+=("${ctx}")
done

if (( ${#missing_vars[@]} )); then
  echo "ERROR: Missing required environment variables:" >&2
  for m in "${missing_vars[@]}"; do echo "  - ${m}" >&2; done
  echo "Please edit env_variables.sh and set these values before proceeding." >&2
elif (( ${#missing_contexts[@]} )); then
  echo "ERROR: The following Kubernetes contexts do not exist:" >&2
  for c in "${missing_contexts[@]}"; do echo "  - ${c}" >&2; done
  kubectl config get-contexts -o name
else
  echo "[ok] All required environment variables are set"
  echo "  Member clusters: ${K8S_CLUSTER_0_CONTEXT_NAME} (index ${SEARCH_CLUSTER_0_INDEX}), ${K8S_CLUSTER_1_CONTEXT_NAME} (index ${SEARCH_CLUSTER_1_INDEX}), ${K8S_CLUSTER_2_CONTEXT_NAME} (index ${SEARCH_CLUSTER_2_INDEX})"
  echo "  Namespace: ${MDB_NAMESPACE}"
  echo "  Source MongoDBMultiCluster: ${RS_RESOURCE_NAME}"
  echo "  MongoDBSearch resource: ${SEARCH_RESOURCE_NAME}"
  echo "  mongot replicas per cluster: ${SEARCH_MONGOT_REPLICAS}"
fi
