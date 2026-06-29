#!/usr/bin/env bash
#
# AWS multi-cluster token-extraction prepare step (E2E_SCENARIO_PLAN.md gap #3).
#
# The e2e harness (docker/mongodb-kubernetes-tests/tests/conftest.py::_get_client_for_cluster)
# does NOT use the kubeconfig `aws eks get-token` exec flow — it reads pre-extracted,
# long-lived ServiceAccount bearer tokens from $MULTI_CLUSTER_CONFIG_DIR. This script
# creates, per cluster, a cluster-admin ServiceAccount + a `kubernetes.io/service-account-token`
# Secret (the same long-lived, OIDC-independent mechanism the operator uses), reads the
# token, and writes the files the harness expects:
#
#   $MULTI_CLUSTER_CONFIG_DIR/
#     central_cluster        -> central context NAME
#     member_cluster_1..N    -> each member context NAME (alphabetical sort order)
#     <context>              -> the long-lived bearer token for that context
#
# All cluster writes go through the K8s API (kubectl), never ad-hoc `aws` CLI mutations.
#
# Usage:
#   source scripts/dev/contexts/e2e_aws_simulated_mc_sharded   # sets KUBECONFIG, MEMBER_CLUSTERS, ...
#   AWS_PROFILE=mck-admin scripts/dev/prepare_aws_mc_tokens.sh

set -Eeou pipefail

: "${KUBECONFIG:?source scripts/dev/contexts/e2e_aws_simulated_mc_sharded first}"
: "${MEMBER_CLUSTERS:?MEMBER_CLUSTERS unset}"
: "${CENTRAL_CLUSTER:?CENTRAL_CLUSTER unset}"
export AWS_PROFILE="${AWS_PROFILE:-mck-admin}"

CONFIG_DIR="${MULTI_CLUSTER_CONFIG_DIR:-${HOME}/.mck-aws-mc-config}"
SA_NAMESPACE="kube-system"
SA_NAME="mck-e2e-admin"
SECRET_NAME="mck-e2e-admin-token"
CRB_NAME="mck-e2e-admin-cluster-admin"

mkdir -p "${CONFIG_DIR}"

# The harness sorts MEMBER_CLUSTERS, and the cluster_index it assigns is that alphabetical
# position. The member_cluster_N pointer files must follow the SAME sort so indexes line up.
read -r -a _members <<<"${MEMBER_CLUSTERS}"
mapfile -t SORTED_MEMBERS < <(printf '%s\n' "${_members[@]}" | sort)

# Every distinct context we need a token for (central may also be a member; dedup).
ALL_CONTEXTS=$(printf '%s\n' "${CENTRAL_CLUSTER}" "${SORTED_MEMBERS[@]}" | sort -u)

extract_token_for_context() {
  local ctx=$1
  echo "[${ctx}] ensuring cluster-admin ServiceAccount + token Secret"

  kubectl --context "${ctx}" -n "${SA_NAMESPACE}" create serviceaccount "${SA_NAME}" \
    --dry-run=client -o yaml | kubectl --context "${ctx}" apply -f - >/dev/null

  kubectl --context "${ctx}" create clusterrolebinding "${CRB_NAME}" \
    --clusterrole=cluster-admin \
    --serviceaccount="${SA_NAMESPACE}:${SA_NAME}" \
    --dry-run=client -o yaml | kubectl --context "${ctx}" apply -f - >/dev/null

  kubectl --context "${ctx}" -n "${SA_NAMESPACE}" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: ${SECRET_NAME}
  annotations:
    kubernetes.io/service-account.name: ${SA_NAME}
type: kubernetes.io/service-account-token
EOF

  # The token controller populates .data.token asynchronously after the Secret is created.
  local token="" i
  for i in $(seq 1 30); do
    token=$(kubectl --context "${ctx}" -n "${SA_NAMESPACE}" get secret "${SECRET_NAME}" \
      -o jsonpath='{.data.token}' 2>/dev/null | base64 --decode 2>/dev/null || true)
    [[ -n "${token}" ]] && break
    sleep 2
  done
  if [[ -z "${token}" ]]; then
    echo "ERROR: [${ctx}] token Secret ${SECRET_NAME} never populated" >&2
    return 1
  fi

  printf '%s' "${token}" >"${CONFIG_DIR}/${ctx}"
  echo "[${ctx}] token written to ${CONFIG_DIR}/${ctx}"
}

for ctx in ${ALL_CONTEXTS}; do
  extract_token_for_context "${ctx}"
done

# Pointer files: contents are context NAMES (the harness then reads <name> for the token).
printf '%s' "${CENTRAL_CLUSTER}" >"${CONFIG_DIR}/central_cluster"
idx=1
for member in "${SORTED_MEMBERS[@]}"; do
  printf '%s' "${member}" >"${CONFIG_DIR}/member_cluster_${idx}"
  echo "member_cluster_${idx} -> ${member}"
  ((idx++))
done

echo "Done. ${CONFIG_DIR} now contains:"
ls -1 "${CONFIG_DIR}"
