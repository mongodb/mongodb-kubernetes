echo "Validating environment variables..."

required_vars=(
  "K8S_CLUSTER_0_CONTEXT_NAME"
  "K8S_CLUSTER_1_CONTEXT_NAME"
  "MDB_NAMESPACE"
  "OM_NAMESPACE"
  "OPERATOR_NAMESPACE"
  "MDB_RESOURCE_NAME"
  "MDB_VERSION"
  "MDB_SHARD_COUNT"
  "MDBS_RESOURCE_NAME"
  "MDBS_CLUSTER_0_NAME"
  "MDBS_CLUSTER_1_NAME"
  "TARGET_CLUSTER_INDEX"
  "MDB_SHARD_0_NAME"
  "MDB_SHARD_1_NAME"
  "MDBS_MONGOT_REPLICAS"
  "MDBS_SEARCH_SYNC_USER_PASSWORD"
  "MDBS_CLUSTER_0_ROUTER_HOSTNAME"
  "MDBS_CLUSTER_1_ROUTER_HOSTNAME"
  "MDBS_CLUSTER_0_EXTERNAL_HOSTNAME_TEMPLATE"
  "MDBS_CLUSTER_1_EXTERNAL_HOSTNAME_TEMPLATE"
  "MDB_MONGOS_HOST_0"
  "MDB_SHARD_0_HOST_0"
)

missing_vars=()
for var in "${required_vars[@]}"; do
  [[ -n "${!var:-}" ]] && [[ "${!var}" != "<"* ]] || missing_vars+=("${var}")
done

if (( ${#missing_vars[@]} )); then
  echo "ERROR: Missing required environment variables:" >&2
  for m in "${missing_vars[@]}"; do echo "  - ${m}" >&2; done
  echo "Please edit env_variables.sh and set these values before proceeding." >&2
else
  for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}"; do
    if ! kubectl config get-contexts "${ctx}" &>/dev/null; then
      echo "ERROR: Kubernetes context '${ctx}' does not exist." >&2
      kubectl config get-contexts -o name
      exit 1
    fi
  done

  echo "[ok] All required environment variables are set"
  echo "  Cluster 0: ${K8S_CLUSTER_0_CONTEXT_NAME} (search name=${MDBS_CLUSTER_0_NAME}, index=${MDBS_CLUSTER_0_INDEX}) -- also hosts the sharded source"
  echo "  Cluster 1: ${K8S_CLUSTER_1_CONTEXT_NAME} (search name=${MDBS_CLUSTER_1_NAME}, index=${MDBS_CLUSTER_1_INDEX})"
  echo "  Namespace: ${MDB_NAMESPACE} (Ops Manager: ${OM_NAMESPACE})"
  echo "  Sharded source (single-cluster): ${MDB_RESOURCE_NAME}, shards: ${MDB_SHARD_0_NAME}, ${MDB_SHARD_1_NAME}"
  echo "  Search resource name: ${MDBS_RESOURCE_NAME}"
  echo "  mongot replicas per (cluster, shard): ${MDBS_MONGOT_REPLICAS}"
  echo "  Source currently routed to search cluster index: ${TARGET_CLUSTER_INDEX}"
fi
