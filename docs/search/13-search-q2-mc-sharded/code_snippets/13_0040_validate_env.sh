echo "Validating environment variables..."

ok=true
[[ -n "${K8S_CENTRAL_CTX:-}" && "${K8S_CENTRAL_CTX}" != "<"* ]] || { echo "  [FAIL] K8S_CENTRAL_CTX not set" >&2; ok=false; }
[[ -n "${K8S_CLUSTER_0_CTX:-}" && "${K8S_CLUSTER_0_CTX}" != "<"* ]] || { echo "  [FAIL] K8S_CLUSTER_0_CTX not set" >&2; ok=false; }
[[ -n "${K8S_CLUSTER_1_CTX:-}" && "${K8S_CLUSTER_1_CTX}" != "<"* ]] || { echo "  [FAIL] K8S_CLUSTER_1_CTX not set" >&2; ok=false; }
[[ -n "${MEMBER_CLUSTER_0_NAME:-}" ]] || { echo "  [FAIL] MEMBER_CLUSTER_0_NAME not set" >&2; ok=false; }
[[ -n "${MEMBER_CLUSTER_1_NAME:-}" ]] || { echo "  [FAIL] MEMBER_CLUSTER_1_NAME not set" >&2; ok=false; }
[[ -n "${MEMBER_CLUSTER_0_REGION:-}" ]] || { echo "  [FAIL] MEMBER_CLUSTER_0_REGION not set" >&2; ok=false; }
[[ -n "${MEMBER_CLUSTER_1_REGION:-}" ]] || { echo "  [FAIL] MEMBER_CLUSTER_1_REGION not set" >&2; ok=false; }
[[ -n "${MDB_NS:-}" ]] || { echo "  [FAIL] MDB_NS not set" >&2; ok=false; }
[[ -n "${MDB_SEARCH_RESOURCE_NAME:-}" ]] || { echo "  [FAIL] MDB_SEARCH_RESOURCE_NAME not set" >&2; ok=false; }
[[ -n "${MDB_EXTERNAL_MONGOS_HOST_0:-}" && "${MDB_EXTERNAL_MONGOS_HOST_0}" != "<"* ]] || { echo "  [FAIL] MDB_EXTERNAL_MONGOS_HOST_0 not set" >&2; ok=false; }
[[ -n "${MDB_EXTERNAL_MONGOS_HOST_1:-}" && "${MDB_EXTERNAL_MONGOS_HOST_1}" != "<"* ]] || { echo "  [FAIL] MDB_EXTERNAL_MONGOS_HOST_1 not set" >&2; ok=false; }
[[ -n "${MDB_SHARD_0_NAME:-}" ]] || { echo "  [FAIL] MDB_SHARD_0_NAME not set" >&2; ok=false; }
[[ -n "${MDB_SHARD_0_HOST_0:-}" && "${MDB_SHARD_0_HOST_0}" != "<"* ]] || { echo "  [FAIL] MDB_SHARD_0_HOST_0 not set" >&2; ok=false; }
[[ -n "${MDB_SHARD_0_HOST_1:-}" && "${MDB_SHARD_0_HOST_1}" != "<"* ]] || { echo "  [FAIL] MDB_SHARD_0_HOST_1 not set" >&2; ok=false; }
[[ -n "${MDB_SHARD_0_HOST_2:-}" && "${MDB_SHARD_0_HOST_2}" != "<"* ]] || { echo "  [FAIL] MDB_SHARD_0_HOST_2 not set" >&2; ok=false; }
[[ -n "${MDB_SHARD_1_NAME:-}" ]] || { echo "  [FAIL] MDB_SHARD_1_NAME not set" >&2; ok=false; }
[[ -n "${MDB_SHARD_1_HOST_0:-}" && "${MDB_SHARD_1_HOST_0}" != "<"* ]] || { echo "  [FAIL] MDB_SHARD_1_HOST_0 not set" >&2; ok=false; }
[[ -n "${MDB_SHARD_1_HOST_1:-}" && "${MDB_SHARD_1_HOST_1}" != "<"* ]] || { echo "  [FAIL] MDB_SHARD_1_HOST_1 not set" >&2; ok=false; }
[[ -n "${MDB_SHARD_1_HOST_2:-}" && "${MDB_SHARD_1_HOST_2}" != "<"* ]] || { echo "  [FAIL] MDB_SHARD_1_HOST_2 not set" >&2; ok=false; }
[[ -n "${MDB_SYNC_PASSWORD_SECRET:-}" ]] || { echo "  [FAIL] MDB_SYNC_PASSWORD_SECRET not set" >&2; ok=false; }
[[ -n "${MDB_EXTERNAL_CA_SECRET:-}" ]] || { echo "  [FAIL] MDB_EXTERNAL_CA_SECRET not set" >&2; ok=false; }
[[ -n "${MDB_KEYFILE_SECRET:-}" ]] || { echo "  [FAIL] MDB_KEYFILE_SECRET not set" >&2; ok=false; }
[[ -n "${MDB_TLS_CERT_SECRET_PREFIX:-}" ]] || { echo "  [FAIL] MDB_TLS_CERT_SECRET_PREFIX not set" >&2; ok=false; }
[[ -n "${MDB_LB_EXTERNAL_HOSTNAME_TEMPLATE:-}" ]] || { echo "  [FAIL] MDB_LB_EXTERNAL_HOSTNAME_TEMPLATE not set" >&2; ok=false; }
[[ "${MDB_LB_EXTERNAL_HOSTNAME_TEMPLATE:-}" == *"{clusterName}"* || "${MDB_LB_EXTERNAL_HOSTNAME_TEMPLATE:-}" == *"{clusterIndex}"* ]] \
  || { echo "  [FAIL] externalHostname template must contain {clusterName} or {clusterIndex} (spec §4.2)" >&2; ok=false; }
[[ "${MDB_LB_EXTERNAL_HOSTNAME_TEMPLATE:-}" == *"{shardName}"* ]] \
  || { echo "  [FAIL] externalHostname template must contain {shardName} for sharded MC (spec §4.2)" >&2; ok=false; }

kubectl config get-contexts "${K8S_CENTRAL_CTX}" &>/dev/null || { echo "  [FAIL] central context '${K8S_CENTRAL_CTX}' missing from kubeconfig" >&2; ok=false; }
kubectl config get-contexts "${K8S_CLUSTER_0_CTX}" &>/dev/null || { echo "  [FAIL] member context '${K8S_CLUSTER_0_CTX}' missing from kubeconfig" >&2; ok=false; }
kubectl config get-contexts "${K8S_CLUSTER_1_CTX}" &>/dev/null || { echo "  [FAIL] member context '${K8S_CLUSTER_1_CTX}' missing from kubeconfig" >&2; ok=false; }

if [[ "${ok}" == "true" ]]; then
  echo "[ok] Environment validated"
  echo "  Central: ${K8S_CENTRAL_CTX}"
  echo "  Members: ${MEMBER_CLUSTER_0_NAME}/${MEMBER_CLUSTER_0_REGION}, ${MEMBER_CLUSTER_1_NAME}/${MEMBER_CLUSTER_1_REGION}"
  echo "  Shards: ${MDB_SHARD_0_NAME}, ${MDB_SHARD_1_NAME}"
  echo "  externalHostname template: ${MDB_LB_EXTERNAL_HOSTNAME_TEMPLATE}"
else
  echo "ERROR: validation failed — fix env_variables.sh and re-source it" >&2
  exit 1
fi
