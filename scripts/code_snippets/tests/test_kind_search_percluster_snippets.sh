#!/usr/bin/env bash

# MongoDB Search, operator-per-cluster with a unified CR (docs/search/12), on
# multi-cluster kind. The task-group setup (setup_kubernetes_environment with
# KUBE_ENVIRONMENT_NAME=multi) has already created the interconnected kind
# clusters with istio and metallb via scripts/dev/recreate_kind_clusters.sh.
#
# The docs treat multi-cluster networking as a prerequisite the reader already
# satisfies: ra-01 provisions clusters and ra-03 installs a service mesh, so
# both are skipped here because the kind environment provides them out of the
# box. ra-04, the connectivity check the docs tell the reader to run against
# their own mesh, is also skipped: the CI hosts cannot satisfy its mesh-DNS
# prerequisite (details at the ra-04 note below); the one Service that needs
# it gets a mirror instead, and the GKE snippets variant still runs ra-04
# verbatim against a real mesh.
#
# This wrapper builds scenario 12's remaining prerequisites with the ra-*
# snippet suites, applying the kind-specific glue documented from the first
# live run, then runs scenario 12's test.sh verbatim.

set -eou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh

script_name=$(readlink -f "${BASH_SOURCE[0]}")

_SNIPPETS_OUTPUT_DIR="$(dirname "${script_name}")/outputs/$(basename "${script_name%.*}")"
export _SNIPPETS_OUTPUT_DIR
mkdir -p "${_SNIPPETS_OUTPUT_DIR}"

# The reference architecture co-locates the central operator with workloads on
# cluster 0, which is the coexistence scenario 12 documents (step 12_0110), so
# cluster 0 is a member cluster and the dedicated kind-e2e-operator cluster is
# deliberately unused.
export K8S_CLUSTER_0_CONTEXT_NAME=kind-e2e-cluster-1
export K8S_CLUSTER_1_CONTEXT_NAME=kind-e2e-cluster-2
export K8S_CLUSTER_2_CONTEXT_NAME=kind-e2e-cluster-3

member_clusters() {
  echo "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"
}

dump_logs() {
  if [[ "${SKIP_DUMP:-"false"}" != "true" ]]; then
    for ctx in $(member_clusters); do
      scripts/evergreen/e2e/dump_diagnostic_information_from_all_namespaces.sh "${ctx}"
    done
  fi
}
trap dump_logs EXIT

# The kind environment ships istio 1.16, which cannot serve the mesh DNS
# prerequisite the docs assume (see the script's header); reinstall a current
# istio on the member clusters before anything else runs.
VERSION="${SNIPPETS_ISTIO_VERSION:-1.30.2}" \
  CTX_CLUSTER1="${K8S_CLUSTER_0_CONTEXT_NAME}" \
  CTX_CLUSTER2="${K8S_CLUSTER_1_CONTEXT_NAME}" \
  CTX_CLUSTER3="${K8S_CLUSTER_2_CONTEXT_NAME}" \
  scripts/code_snippets/install_istio_for_snippets.sh

source public/architectures/setup-multi-cluster/ra-02-setup-operator/env_variables.sh

# Phase 1: central hub-and-spoke operator (ra-02). Its first snippet creates the
# namespaces with a plain `kubectl create`, so nothing may pre-create them.
./public/architectures/setup-multi-cluster/ra-02-setup-operator/test.sh

# Label the workload namespaces for istio sidecar injection, replicating
# ra-03_0050_label_namespaces.sh from the skipped mesh-install suite. This must
# happen BEFORE any workload pod exists (the first ones arrive with ra-06), so
# every pod carries its sidecar from birth, matching the validated GKE and
# kind runs. (On a working mesh the sidecar's DNS proxy is also what resolves
# remote clusters' Services; on these CI hosts it is not -- see the ra-04
# note below.)
# The operator namespace is deliberately not labeled: the central operator is
# restarted below and would come back with a sidecar, unlike on the validated
# GKE and kind runs where it runs without one.
for ctx in $(member_clusters); do
  for ns in "${OM_NAMESPACE}" "${MDB_NAMESPACE}"; do
    kubectl --context "${ctx}" label namespace "${ns}" istio-injection=enabled --overwrite
  done
done

# `kubectl mongodb multicluster setup` (ra-02_0200) copies the API server URLs
# from the host kubeconfig into the operator's kubeconfig Secret. On kind those
# are https://127.0.0.1:<port> -- reachable from the host, not from pods.
# Rewrite each cluster's server to its in-cluster `kubernetes` Service clusterIP,
# which is routable across interconnected kind clusters (same rewrite the e2e
# harness does in configure_multi_cluster_environment, scripts/funcs/multicluster).
operator_kubeconfig_secret="mongodb-enterprise-operator-multi-cluster-kubeconfig"
tmp_kubeconfig=$(mktemp)
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${OPERATOR_NAMESPACE}" \
  get secret "${operator_kubeconfig_secret}" -o jsonpath='{.data.kubeconfig}' | base64 -d > "${tmp_kubeconfig}"
for ctx in $(member_clusters); do
  api_server="https://$(kubectl get svc --context "${ctx}" -n default kubernetes -o jsonpath='{.spec.clusterIP}')"
  kubectl config --kubeconfig "${tmp_kubeconfig}" set "clusters.${ctx}.server" "${api_server}"
done
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${OPERATOR_NAMESPACE}" \
  create secret generic "${operator_kubeconfig_secret}" \
  --from-file=kubeconfig="${tmp_kubeconfig}" --dry-run=client -o yaml | \
  kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${OPERATOR_NAMESPACE}" apply -f -
rm -f "${tmp_kubeconfig}"

# MongoDB 8.3.x (ra-07 below) requires a newer automation agent than the
# published chart's default. Drop the pin once the chart default catches up.
AGENT_VERSION="${AGENT_VERSION:-108.0.13.8870-1}"
helm upgrade mongodb-kubernetes-operator-multi-cluster "${OPERATOR_HELM_CHART}" \
  --kube-context "${K8S_CLUSTER_0_CONTEXT_NAME}" \
  --namespace "${OPERATOR_NAMESPACE}" \
  --reuse-values \
  --set agent.version="${AGENT_VERSION}"

kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${OPERATOR_NAMESPACE}" \
  rollout restart deployment mongodb-kubernetes-operator-multi-cluster
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${OPERATOR_NAMESPACE}" \
  rollout status deployment mongodb-kubernetes-operator-multi-cluster --timeout=300s

# ra-04, the docs' connectivity check, is deliberately NOT run here. It
# verifies the mesh prerequisite that a Service existing in only ONE cluster
# resolves by name from pods in the others. On the Evergreen task hosts
# istio's DNS proxying of such remote-only Services never resolves -- with
# istio 1.16 and 1.30 alike, across kind node images -- while the identical
# setup passes elsewhere (and the GKE snippets variant still runs ra-04
# verbatim against a real mesh). The check is unsatisfiable on this
# substrate, not optional in the docs: a customer's mesh must still pass it.
# The one name in this pipeline that needs remote resolution, om-svc during
# ra-06, is provided by the mirror below instead.

# The cert-manager helm install returns before the admission webhook serves,
# so the ClusterIssuer apply right after it can be refused on a cold cluster.
# On retry the suite's run log resumes from the failed snippet.
ra05_test=public/architectures/setup-multi-cluster/ra-05-setup-cert-manager/test.sh
for attempt in 1 2 3; do
  if "./${ra05_test}"; then
    break
  fi
  if [[ "${attempt}" == 3 ]]; then
    echo "ra-05 cert-manager setup failed after ${attempt} attempts"
    exit 1
  fi
  echo "ra-05 cert-manager setup failed (attempt ${attempt}); retrying"
  sleep 30
done

# ra-06's Application Database spans all three clusters, and its agents on
# clusters 2 and 3 reach Ops Manager by the name om-svc, which exists only on
# cluster 0 -- exactly the remote-only resolution the CI mesh cannot provide
# (see the ra-04 note above). Mirror the Service instead: in the other two
# clusters, a selectorless Service named om-svc plus an EndpointSlice whose
# endpoint is cluster 0's om-svc clusterIP; Service IPs are routable across
# the interconnected kind clusters. Runs in the background because om-svc
# only appears once ra-06 has applied its Ops Manager resource.
mirror_om_svc() {
  local om_ip=""
  for _ in $(seq 1 90); do
    om_ip=$(kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${OM_NAMESPACE}" \
      get svc om-svc -o jsonpath='{.spec.clusterIP}' 2>/dev/null || true)
    if [[ -n "${om_ip}" ]]; then break; fi
    sleep 10
  done
  if [[ -z "${om_ip}" ]]; then
    echo "om-svc mirror: om-svc never appeared on cluster 0; nothing mirrored" >&2
    return 0
  fi
  # copy the real Service's ports: the endpoint is cluster 0's Service VIP,
  # which listens on the service ports themselves (8443 with ra-06's TLS)
  local ports
  ports=$(kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${OM_NAMESPACE}" \
    get svc om-svc -o json | jq -c '[.spec.ports[] | {name, protocol, port}]')
  for ctx in "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
    jq -n --arg ns "${OM_NAMESPACE}" --arg ip "${om_ip}" --argjson ports "${ports}" '
      {apiVersion: "v1", kind: "List", items: [
        {apiVersion: "v1", kind: "Service",
         metadata: {name: "om-svc", namespace: $ns},
         spec: {ports: $ports}},
        {apiVersion: "discovery.k8s.io/v1", kind: "EndpointSlice",
         metadata: {name: "om-svc-mirror", namespace: $ns,
                    labels: {"kubernetes.io/service-name": "om-svc"}},
         addressType: "IPv4",
         ports: $ports,
         endpoints: [{addresses: [$ip]}]}
      ]}' | kubectl --context "${ctx}" apply -f -
  done
  echo "om-svc mirror: om-svc (${om_ip}) mirrored into the other member clusters"
}
mirror_om_svc &

# Phase 1: Ops Manager (ra-06) -- the slow step
source public/architectures/ra-06-ops-manager-multi-cluster/env_variables.sh
export OPS_MANAGER_VERSION=8.0.25 # Search minimum; ra-06 defaults to 8.0.5
./public/architectures/ra-06-ops-manager-multi-cluster/test.sh

# Phase 1: the source replica set (ra-07)
source public/architectures/ra-07-mongodb-replicaset-multi-cluster/env_variables.sh
export MONGODB_VERSION=8.3.4-ent # Search minimum is 8.3.0; the 8.0.5-ent default lacks the searchCoordinator role
./public/architectures/ra-07-mongodb-replicaset-multi-cluster/test.sh

# Phase 2: scenario 12 (operator-per-cluster Search), snippets verbatim
test_dir="./docs/search/12-search-percluster-operator-rs"
source "${test_dir}/env_variables.sh"
echo "Sourcing env variables for ${CODE_SNIPPETS_FLAVOR} flavor"
# shellcheck disable=SC1090
test -f "${test_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh" && source "${test_dir}/env_variables_${CODE_SNIPPETS_FLAVOR}.sh"
${test_dir}/test.sh
