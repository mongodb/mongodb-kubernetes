#!/usr/bin/env bash
#
# Minimal e2e environment bootstrap for the AWS real-infra multi-cluster run.
#
# The canonical bootstrap (scripts/dev/prepare_local_e2e_run.sh) is kind-specific (it does
# `kubectl get nodes | grep control-plane`, istio labelling, a reset, etc.) and aborts on
# EKS. This does just the cluster-side prerequisites the harness fixtures assume already
# exist, for the four EKS clusters:
#   - the test namespace (+ evg=task label) on central + every member;
#   - `image-registries-secret` (docker-registry) for the private ECR the images live in
#     (us-east-1 ECR; clusters are eu-south-1, same account) on every cluster/namespace;
#   - the `mongodb-kubernetes-database-pods` / `mongodb-kubernetes-appdb` ServiceAccounts
#     (with Helm ownership metadata so the operator chart adopts them);
#   - the `operator-installation-config` ConfigMap on the central cluster.
#
# CRDs + the cross-cluster kubeconfig Secret are installed by the operator Helm releases /
# run_kube_config_creation_tool that the test fixtures invoke, so they are NOT done here.
#
# Usage (context must already be switched: scripts/dev/switch_context.sh e2e_aws_simulated_mc_sharded):
#   AWS_PROFILE=mck-admin scripts/dev/bootstrap_aws_mc_e2e.sh

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

cd "$(git rev-parse --show-toplevel)"
# Preload the generated env (REGISTRY + base vars), then source the AWS context fresh so its
# overrides (operator/search image pins, NAMESPACE, CENTRAL_CLUSTER, ...) win.
set -a
# shellcheck disable=SC1090,SC1091
source .generated/context.env
set +a
# shellcheck disable=SC1091
source scripts/dev/contexts/e2e_aws_simulated_mc_sharded
# Apply the devc network-prefix to the namespace inline: the prefix tooling lives in the
# devcontainer layer (not carried by this AWS-only branch), and private-context resets
# NAMESPACE to the un-prefixed base. WATCH_NAMESPACE must match or the operator cache-syncs
# the wrong namespace and times out.
if [[ -n "${MCK_DEVC_NET_PREFIX:-}" ]]; then
  export NAMESPACE="${NAMESPACE}-${MCK_DEVC_NET_PREFIX}"
  export WATCH_NAMESPACE="${NAMESPACE}"
fi
# shellcheck disable=SC1091
source scripts/funcs/printing
source scripts/funcs/operator_deployment

: "${NAMESPACE:?NAMESPACE unset — switch_context first}"
: "${MEMBER_CLUSTERS:?MEMBER_CLUSTERS unset}"
: "${CENTRAL_CLUSTER:?CENTRAL_CLUSTER unset}"
export AWS_PROFILE="${AWS_PROFILE:-mck-admin}"

ECR_HOST="268558157000.dkr.ecr.us-east-1.amazonaws.com"
ECR_REGION="us-east-1"
HELM_RELEASE_NAME="${OPERATOR_NAME:-mongodb-kubernetes-operator}"

ALL_CONTEXTS=$(printf '%s\n' "${CENTRAL_CLUSTER}" ${MEMBER_CLUSTERS} | sort -u)

echo "==> ECR login token (${ECR_HOST}, ${ECR_REGION})"
ECR_PW="$(aws ecr get-login-password --region "${ECR_REGION}")"

for ctx in ${ALL_CONTEXTS}; do
  echo "==> [${ctx}] namespace ${NAMESPACE}"
  kubectl --context "${ctx}" create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl --context "${ctx}" apply -f -
  kubectl --context "${ctx}" label namespace "${NAMESPACE}" evg=task --overwrite

  echo "==> [${ctx}] image-registries-secret"
  kubectl --context "${ctx}" -n "${NAMESPACE}" create secret docker-registry image-registries-secret \
    --docker-server="${ECR_HOST}" --docker-username=AWS --docker-password="${ECR_PW}" \
    --dry-run=client -o yaml | kubectl --context "${ctx}" apply -f -

  echo "==> [${ctx}] database-pod ServiceAccounts"
  for sa in mongodb-kubernetes-database-pods mongodb-kubernetes-appdb; do
    kubectl --context "${ctx}" -n "${NAMESPACE}" create serviceaccount "${sa}" --dry-run=client -o yaml \
      | kubectl --context "${ctx}" apply -f -
    kubectl --context "${ctx}" -n "${NAMESPACE}" label serviceaccount "${sa}" "app.kubernetes.io/managed-by=Helm" --overwrite
    kubectl --context "${ctx}" -n "${NAMESPACE}" annotate serviceaccount "${sa}" \
      "meta.helm.sh/release-name=${HELM_RELEASE_NAME}" \
      "meta.helm.sh/release-namespace=${NAMESPACE}" --overwrite
    # Wire the pull secret onto the SA so DB/mongot/OM pods can pull from the private ECR.
    kubectl --context "${ctx}" -n "${NAMESPACE}" patch serviceaccount "${sa}" \
      -p '{"imagePullSecrets":[{"name":"image-registries-secret"}]}'
  done
done

echo "==> [${CENTRAL_CLUSTER}] operator-installation-config ConfigMap"
prepare_operator_config_map "${CENTRAL_CLUSTER}"

# The harness (conftest get_api_servers_from_test_pod_kubeconfig) reads this secret on the
# test-pod cluster to discover each member cluster's API server URL when LOCAL_OPERATOR=false.
# Auth still uses the long-lived bearer tokens in MULTI_CLUSTER_CONFIG_DIR (kube-system SA),
# so the exec-based merged kubeconfig is fine here — only the server URLs are parsed out.
# `make reset` wipes namespaced secrets, so recreate it on every bootstrap.
: "${test_pod_cluster:?test_pod_cluster unset}"
echo "==> [${test_pod_cluster}] test-pod-kubeconfig secret (member API-server discovery)"
kubectl --context "${test_pod_cluster}" -n "${NAMESPACE}" delete secret test-pod-kubeconfig --ignore-not-found
kubectl --context "${test_pod_cluster}" -n "${NAMESPACE}" create secret generic test-pod-kubeconfig \
  --from-file=kubeconfig="${KUBECONFIG}"

echo "Bootstrap complete."
