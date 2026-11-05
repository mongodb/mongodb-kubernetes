# E2E Test Environment Overrides (Enterprise with in-cluster Ops Manager, multi-cluster kind)
#
# Sourced by the CI runner to override environment for automated E2E testing.
# Do not use this file for manual testing.
# NOTE: uses the 3-cluster kind context (central + two AppDB member clusters).

source "${PROJECT_DIR}/scripts/funcs/operator_deployment"
source "${PROJECT_DIR}/scripts/dev/contexts/e2e_multi_cluster_om_appdb"

# Central cluster is CENTRAL_CLUSTER; the two AppDB member clusters are the
# 2nd and 3rd words of MEMBER_CLUSTERS.
export K8S_CTX_0="${CENTRAL_CLUSTER}"
K8S_CTX_1=$(echo "${MEMBER_CLUSTERS}" | awk '{print $2}')
export K8S_CTX_1
K8S_CTX_2=$(echo "${MEMBER_CLUSTERS}" | awk '{print $3}')
export K8S_CTX_2

# kind API server endpoints (127.0.0.1:<port>) are not reachable from pods, so
# the kubeconfig Secret created by `kubectl mongodb multicluster setup` would
# leave the operator unable to reach the member clusters. Build a kubeconfig
# variant pointing at each cluster's node container IP on the shared docker
# network (port 6443), reachable both from pods and from this host.
plugin_kubeconfig="${PROJECT_DIR}/.generated/snippets_mc_appdb_plugin_kubeconfig"
mkdir -p "$(dirname "${plugin_kubeconfig}")"
cp "${KUBECONFIG:-${HOME}/.kube/config}" "${plugin_kubeconfig}"
for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}" "${K8S_CTX_2}"; do
  node_ip=$(kubectl get nodes --context "${ctx}" -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')
  kubectl config --kubeconfig "${plugin_kubeconfig}" set "clusters.${ctx}.server" "https://${node_ip}:6443"
done
export KUBECONFIG="${plugin_kubeconfig}"

OPERATOR_ADDITIONAL_HELM_VALUES="$(get_operator_helm_values | tr ' ' ',')"
export OPERATOR_ADDITIONAL_HELM_VALUES
export OPERATOR_HELM_CHART="${PROJECT_DIR}/helm_chart"

source "$(dirname "${BASH_SOURCE[0]}")/env_variables_e2e_common.sh"
