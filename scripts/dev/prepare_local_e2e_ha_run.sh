#!/usr/bin/env bash

# prepare_local_e2e_ha_run.sh
#
# Prepares an HA multi-cluster environment for local e2e runs.
#
# Unlike scripts/dev/prepare_local_e2e_run.sh, this script has NO concept of a
# "central" or "bootstrap" cluster — every cluster listed in HA_CLUSTERS is an
# equal Raft peer. Each operator independently calls hashicorp/raft's
# BootstrapCluster() with the same voter list at startup; the API is safe to
# call from every node, so no designated bootstrap is required.
#
# Required env vars:
#   HA_CLUSTERS    Space-separated list of kubeconfig context names that
#                  together form the Raft membership. Minimum 3 for quorum.
#                  Example: HA_CLUSTERS="kind-cluster-1 kind-cluster-2 kind-cluster-3"
#
# Optional env vars:
#   NAMESPACE         Operator namespace. Inherited from set_env_context.sh.
#   DEPLOY_OPERATOR   If "true", run "helm upgrade --install" on every peer.
#   LOCAL_OPERATOR    If "true", set operator.replicas=0 on every peer's Helm
#                     release (developer runs the operator process locally).

set -Eeou pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh
source scripts/funcs/printing
source scripts/funcs/operator_deployment
source scripts/funcs/kubernetes

if [[ "$(uname)" == "Linux" ]]; then
  export PATH=/opt/golang/go1.25/bin:${PATH}
  export GOROOT=/opt/golang/go1.25
fi

on_exit() {
  # shellcheck disable=SC2181
  error_code=$?
  if [[ ${error_code} -ne 0 ]]; then
    echo
    echo "An error occurred during execution. Execute the script again."
    echo
    exit ${error_code}
  fi
}

trap on_exit EXIT

# ---------- HA cluster enumeration ----------

: "${HA_CLUSTERS:?HA_CLUSTERS must be set (space-separated context list, minimum 3 clusters for Raft quorum)}"

read -ra HA_CLUSTERS_ARR <<< "${HA_CLUSTERS}"
if [[ "${#HA_CLUSTERS_ARR[@]}" -lt 3 ]]; then
  echo "WARNING: HA_CLUSTERS has fewer than 3 entries; Raft requires a majority quorum, so 2-node clusters cannot tolerate a single failure." >&2
fi

# Arbitrary anchor used only for non-symmetric local steps (e.g. choosing which
# context the local kubectl points at for "make install" and reset). No Raft
# semantics — every cluster is an equal peer.
default_ctx="${HA_CLUSTERS_ARR[0]}"

echo "HA Raft membership:"
for ctx in "${HA_CLUSTERS_ARR[@]}"; do
  echo "  - ${ctx}"
done
echo

# ---------- Reset ----------

if [[ "${RESET:-"true"}" == "true" ]]; then
  echo "Running reset script..."
  go build -o "${PROJECT_DIR}/bin/reset" "${PROJECT_DIR}/scripts/dev/reset/"
  "${PROJECT_DIR}/bin/reset" 2>&1 | prepend "reset"
fi

# Pick one context to use as the local kubectl default. Steps that don't
# iterate over peers (e.g. "make install" applying CRD manifests) act against
# whichever cluster kubectl is currently pointed at; subsequent per-cluster
# loops apply the same manifests on the other peers.
(
  kubectl config set-context "${default_ctx}" "--namespace=${NAMESPACE}" &>/dev/null || true
  kubectl config use-context "${default_ctx}"
  echo "Current context: ${default_ctx}, namespace=${NAMESPACE}"
  kubectl get nodes | grep "control-plane" || true
) 2>&1 | prepend "set current context"

# ---------- Per-cluster namespace ----------

echo "Ensuring namespace ${NAMESPACE} on every HA peer"
for ctx in "${HA_CLUSTERS_ARR[@]}"; do
  (
    kubectl config use-context "${ctx}" &>/dev/null
    ensure_namespace "${NAMESPACE}"
  ) 2>&1 | prepend "ensure_namespace[${ctx}]"
done
kubectl config use-context "${default_ctx}" &>/dev/null

# ---------- Background tasks ----------

# make install installs CRDs from the current context's kubeconfig. The
# kubectl-mongodb setup below will also create them on other clusters.
(make install 2>&1 | prepend "make install") &
pid_install=$!
(scripts/dev/delete_om_projects.sh 2>&1 | prepend "delete_om_projects") &
pid_om=$!

echo "Configuring container auth (skips login if credentials still valid)"
scripts/dev/configure_container_auth.sh 2>&1 | prepend "configure_docker_auth"

echo "Configuring operator"
scripts/evergreen/e2e/configure_operator.sh 2>&1 | prepend "configure_operator"

# Each operator reads the operator config map from its own cluster, so create
# it on every peer.
for ctx in "${HA_CLUSTERS_ARR[@]}"; do
  echo "Preparing operator config map on ${ctx}"
  prepare_operator_config_map "${ctx}" 2>&1 | prepend "prepare_operator_config_map[${ctx}]"
done

rm -rf docker/mongodb-kubernetes-tests/helm_chart
cp -rf helm_chart docker/mongodb-kubernetes-tests/helm_chart

# ---------- kubectl-mongodb multicluster setup ----------

# kubectl-mongodb's CLI still uses --central-cluster / --member-clusters as
# argument names; the labels are legacy. We pass HA_CLUSTERS[0] as
# --central-cluster purely because the CLI requires a single value there. All
# clusters end up with identical RBAC, kubeconfig Secret, and raft-* ConfigMaps.
cli_central="${HA_CLUSTERS_ARR[0]}"
cli_member_csv="$(echo "${HA_CLUSTERS}" | tr ' ' ',')"

echo "Building kubectl-mongodb plugin"
go build -o "${PROJECT_DIR}/bin/kubectl-mongodb" "${PROJECT_DIR}/cmd/kubectl-mongodb"

params=(
  "--central-cluster" "${cli_central}"
  "--member-clusters" "${cli_member_csv}"
  "--member-cluster-namespace" "${NAMESPACE}"
  "--central-cluster-namespace" "${NAMESPACE}"
  "--service-account" "mongodb-kubernetes-operator"
  "--create-service-account-secrets"
  "--member-clusters-api-servers" "https://10.97.0.1,https://10.98.0.1,https://10.99.0.1"
)
if [[ "${OPERATOR_CLUSTER_SCOPED:-"false"}" == "true" ]]; then
  params+=("--cluster-scoped")
fi

echo "Running kubectl-mongodb multicluster setup"
"${PROJECT_DIR}/bin/kubectl-mongodb" multicluster setup "${params[@]}" 2>&1 | prepend "kubectl_mongodb_setup"

# Wait for background operations.
wait "${pid_install}" || exit $?
wait "${pid_om}" || exit $?
test -f "docker/mongodb-kubernetes-tests/.test_identifiers" && rm "docker/mongodb-kubernetes-tests/.test_identifiers"

# ---------- Database SA bootstrap (per cluster) ----------

# Each MongoDB workload pod needs database/appdb SAs on the cluster it runs in.
# In HA we don't know in advance which cluster will host workloads, so ensure
# them everywhere with Helm ownership metadata for later adoption.
for ctx in "${HA_CLUSTERS_ARR[@]}"; do
  kubectl --context "${ctx}" config use-context "${ctx}" &>/dev/null || true
  for sa in mongodb-kubernetes-database-pods mongodb-kubernetes-appdb; do
    kubectl --context "${ctx}" create serviceaccount "${sa}" -n "${NAMESPACE}" --dry-run=client -o yaml | kubectl --context "${ctx}" apply -f -
    kubectl --context "${ctx}" label serviceaccount "${sa}" -n "${NAMESPACE}" "app.kubernetes.io/managed-by=Helm" --overwrite
    kubectl --context "${ctx}" annotate serviceaccount "${sa}" -n "${NAMESPACE}" \
      "meta.helm.sh/release-name=mongodb-kubernetes-operator" \
      "meta.helm.sh/release-namespace=${NAMESPACE}" \
      --overwrite
  done
done

# ---------- Helm install per cluster ----------

(
  if [[ "${DEPLOY_OPERATOR:-"false"}" == "true" ]]; then
    # shellcheck disable=SC2178
    helm_values=$(get_operator_helm_values)
    # shellcheck disable=SC2179
    if [[ "${LOCAL_OPERATOR:-"false"}" == "true" ]]; then
      helm_values+=" operator.replicas=0"
    fi
    # shellcheck disable=SC2128
    helm_set_flag="$(echo "${helm_values}" | tr ' ' ',')"

    for ctx in "${HA_CLUSTERS_ARR[@]}"; do
      echo "==> helm upgrade --install on context ${ctx}"
      helm upgrade --install --kube-context "${ctx}" \
        mongodb-kubernetes-operator helm_chart \
        --namespace "${NAMESPACE}" \
        --set "${helm_set_flag}"
    done
  fi
) 2>&1 | prepend "deploy operator"

# ---------- imagePullSecrets per cluster (kind only) ----------

(
  if [[ "${KUBE_ENVIRONMENT_NAME}" == "kind" || "${KUBE_ENVIRONMENT_NAME}" == "multi" ]]; then
    echo "patching default SAs with imagePullSecrets on every peer"
    for ctx in "${HA_CLUSTERS_ARR[@]}"; do
      service_accounts=$(kubectl --context "${ctx}" get serviceaccounts -n "${NAMESPACE}" -o jsonpath='{.items[*].metadata.name}')
      for service_account in ${service_accounts}; do
        kubectl --context "${ctx}" patch serviceaccount "${service_account}" -n "${NAMESPACE}" \
          -p "{\"imagePullSecrets\": [{\"name\": \"image-registries-secret\"}]}" || true
      done
    done
  fi
) 2>&1 | prepend "patch service accounts"

echo
echo "HA prepare complete. Peers: ${HA_CLUSTERS}"
