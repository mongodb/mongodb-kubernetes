# E2E Test Environment Overrides (Enterprise with Ops Manager, multi-cluster kind)
#
# This file is sourced by the test runner to override environment variables
# for automated E2E testing. Do not use this file for manual testing.
# NOTE: This uses the 2-cluster kind context with cloud-qa Ops Manager.

source "${PROJECT_DIR}/scripts/funcs/operator_deployment"
source "${PROJECT_DIR}/scripts/dev/contexts/e2e_multi_cluster_2_clusters"
source "${PROJECT_DIR}/scripts/dev/contexts/variables/mongodb_search_dev"

# Member cluster contexts come from the e2e context: CENTRAL_CLUSTER is also
# member 0, the second word of MEMBER_CLUSTERS is member 1.
export K8S_CTX_0="${CENTRAL_CLUSTER}"
K8S_CTX_1=$(echo "${MEMBER_CLUSTERS}" | awk '{print $2}')
export K8S_CTX_1

# kind API server endpoints (127.0.0.1:<port>) are not reachable from pods, so
# the kubeconfig Secret created by `kubectl mongodb multicluster setup` would
# leave the operator unable to reach the member clusters. Build a kubeconfig
# variant pointing at each cluster's node container IP on the shared docker
# network (port 6443). Unlike the kubernetes Service clusterIP that the e2e
# test-pod flow uses, the node IP is reachable BOTH from pods (the kind
# interconnect routes via node IPs) and from this host (docker bridge) -- the
# plugin itself talks to the API servers from the host while it runs.
MDB_PLUGIN_KUBECONFIG="${PROJECT_DIR}/.generated/snippets_plugin_kubeconfig"
cp "${KUBECONFIG:-${HOME}/.kube/config}" "${MDB_PLUGIN_KUBECONFIG}"
for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}"; do
  node_ip=$(kubectl get nodes --context "${ctx}" -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')
  kubectl config --kubeconfig "${MDB_PLUGIN_KUBECONFIG}" set "clusters.${ctx}.server" "https://${node_ip}:6443"
done
export MDB_PLUGIN_KUBECONFIG

OPERATOR_ADDITIONAL_HELM_VALUES="$(get_operator_helm_values | tr ' ' ',')"
export OPERATOR_ADDITIONAL_HELM_VALUES
export OPERATOR_HELM_CHART="${PROJECT_DIR}/helm_chart"

# we need project name with a timestamp (NAMESPACE in evg is randomized) to allow for cloud-qa cleanups
export OPS_MANAGER_PROJECT_NAME="${NAMESPACE}-${MDB_RESOURCE_NAME}"
export OPS_MANAGER_API_URL="${OM_BASE_URL}"
export OPS_MANAGER_API_USER="${OM_USER}"
export OPS_MANAGER_API_KEY="${OM_API_KEY}"
export OPS_MANAGER_ORG_ID="${OM_ORGID}"

# Override the user-supplied mongos and shard host placeholders with the
# operator-managed Services created by the internal_ steps.
# mongos naming: <resource>-mongos-<clusterIndex>-<memberIndex>-svc
export MDB_EXTERNAL_MONGOS_HOST_0="${MDB_RESOURCE_NAME}-mongos-0-0-svc.${MDB_NS}.svc.cluster.local:27017"
export MDB_EXTERNAL_MONGOS_HOST_1="${MDB_RESOURCE_NAME}-mongos-1-0-svc.${MDB_NS}.svc.cluster.local:27017"
export MDB_ADMIN_CONNECTION_STRING="mongodb://mdb-admin:${MDB_ADMIN_USER_PASSWORD}@${MDB_EXTERNAL_MONGOS_HOST_0},${MDB_EXTERNAL_MONGOS_HOST_1}/?tls=true&tlsCAFile=/tls/ca.crt&authSource=admin&authMechanism=SCRAM-SHA-256"
export MDB_USER_CONNECTION_STRING="mongodb://mdb-user:${MDB_USER_PASSWORD}@${MDB_EXTERNAL_MONGOS_HOST_0},${MDB_EXTERNAL_MONGOS_HOST_1}/?tls=true&tlsCAFile=/tls/ca.crt&authSource=admin&authMechanism=SCRAM-SHA-256"

# Shard member host naming: <resource>-<shardIndex>-<clusterIndex>-<memberIndex>-svc
export MDB_EXTERNAL_SHARD_0_HOST_CL0="${MDB_RESOURCE_NAME}-0-0-0-svc.${MDB_NS}.svc.cluster.local:27017"
export MDB_EXTERNAL_SHARD_0_HOST_CL1="${MDB_RESOURCE_NAME}-0-1-0-svc.${MDB_NS}.svc.cluster.local:27017"
export MDB_EXTERNAL_SHARD_1_HOST_CL0="${MDB_RESOURCE_NAME}-1-0-0-svc.${MDB_NS}.svc.cluster.local:27017"
export MDB_EXTERNAL_SHARD_1_HOST_CL1="${MDB_RESOURCE_NAME}-1-1-0-svc.${MDB_NS}.svc.cluster.local:27017"
export MDB_EXTERNAL_SHARD_2_HOST_CL0="${MDB_RESOURCE_NAME}-2-0-0-svc.${MDB_NS}.svc.cluster.local:27017"
export MDB_EXTERNAL_SHARD_2_HOST_CL1="${MDB_RESOURCE_NAME}-2-1-0-svc.${MDB_NS}.svc.cluster.local:27017"
