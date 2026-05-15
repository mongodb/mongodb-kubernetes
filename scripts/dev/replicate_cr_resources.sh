#!/usr/bin/env bash
#
# replicate_cr_resources.sh — Phase D D'3.
#
# Copy every spec-referenced K8s resource of a MongoDB CR from a SOURCE
# kubeconfig (typically the operator/hub cluster where the e2e fixture
# created the resources) into N TARGET member-cluster kubeconfigs so all
# member clusters hold byte-identical copies. Required because Phase F12
# makes cross-cluster K8s replication a no-op in distributed mode — the
# resource-hash agreement gate refuses to advance until every operator's
# local FSM observation of every referenced resource has the same content
# hash.
#
# Resource set (mirrors collectSpecReferencedResourceRefs in
# controllers/operator/distributed_resource_agreement.go):
#   - Project ConfigMap (spec.cloudManager.configMapRef.name or
#     spec.opsManager.configMapRef.name).
#   - Credentials Secret (spec.credentials).
#   - Member / agent / prometheus certificate Secrets, if any of TLS or
#     certificatesSecretsPrefix is set.
#   - LDAP bind-query Secret, agent-password Secret, if referenced.
#
# CA bundle ConfigMap referenced inside the project CM
# (sslMMSCAConfigMap) is NOT yet in the agreed set in F12 — it's resolved
# downstream. We replicate it best-effort if present, so a TLS run doesn't
# trip on it later. (TODO post-PoC: drive this from the operator's view.)
#
# After copying, the script SHA-256-hashes the .data of every replicated
# ConfigMap / Secret on each target cluster and asserts the hashes agree.
# Mismatch aborts loudly.
#
# Usage:
#   scripts/dev/replicate_cr_resources.sh <crNamespace> <crName>
#
# Env vars:
#   SRC_KUBECONFIG       Source kubeconfig (defaults to
#                        .generated/current.devc.kubeconfig); read with
#                        --context if SRC_CONTEXT is set.
#   SRC_CONTEXT          Source context name (e.g. "kind-e2e-operator"). If
#                        unset, the SRC_KUBECONFIG's current-context is used.
#   TARGET_KUBECONFIGS   Space-separated list of target kubeconfig files
#                        (defaults to .generated/cluster-1.kubeconfig
#                        .generated/cluster-2.kubeconfig
#                        .generated/cluster-3.kubeconfig — produced by D'2).
#
# Exit codes:
#   0  all resources replicated, all hashes agree
#   1  CR not found, ref-resolution failed, copy failed, or hashes diverged

set -Eeuo pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

usage() {
  sed -n '3,55p' "$0"
}

if [[ $# -lt 2 ]]; then
  usage
  exit 1
fi

CR_NS="$1"
CR_NAME="$2"

cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"

SRC_KUBECONFIG="${SRC_KUBECONFIG:-.generated/current.devc.kubeconfig}"
SRC_CONTEXT="${SRC_CONTEXT:-}"
TARGET_KUBECONFIGS="${TARGET_KUBECONFIGS:-.generated/cluster-1.kubeconfig .generated/cluster-2.kubeconfig .generated/cluster-3.kubeconfig}"

if [[ ! -f "${SRC_KUBECONFIG}" ]]; then
  echo "ERROR: source kubeconfig not found: ${SRC_KUBECONFIG}" >&2
  exit 1
fi

# Wrapper that runs kubectl against the source kubeconfig (+optional context).
src_kubectl() {
  local args=(--kubeconfig "${SRC_KUBECONFIG}")
  if [[ -n "${SRC_CONTEXT}" ]]; then
    args+=(--context "${SRC_CONTEXT}")
  fi
  kubectl "${args[@]}" "$@"
}

# Validate target kubeconfigs.
for kc in ${TARGET_KUBECONFIGS}; do
  if [[ ! -f "${kc}" ]]; then
    echo "ERROR: target kubeconfig not found: ${kc}" >&2
    exit 1
  fi
done

# Resolve all spec-referenced refs from the source CR. Each line of the
# emitted file is "Kind/Namespace/Name". Namespace can be the CR's ns or
# anything spec'd in a ConfigMapRef.
refs_file="$(mktemp)"
trap 'rm -f "${refs_file}"' EXIT

cr_json=""
if ! cr_json="$(src_kubectl get mdb -n "${CR_NS}" "${CR_NAME}" -o json 2>/dev/null)" || [[ -z "${cr_json}" ]]; then
  echo "ERROR: MongoDB ${CR_NS}/${CR_NAME} not found in source kubeconfig ${SRC_KUBECONFIG}" >&2
  exit 1
fi

# Helper: append a ref line (skip empty name).
append_ref() {
  local kind="$1" ns="$2" name="$3"
  if [[ -z "${name}" || "${name}" == "null" ]]; then
    return 0
  fi
  if [[ -z "${ns}" || "${ns}" == "null" ]]; then
    ns="${CR_NS}"
  fi
  echo "${kind}/${ns}/${name}" >> "${refs_file}"
}

# Project ConfigMap (cloudManager OR opsManager).
project_cm_name="$(jq -r '.spec.cloudManager.configMapRef.name // .spec.opsManager.configMapRef.name // ""' <<<"${cr_json}")"
project_cm_ns="$(jq -r '.spec.cloudManager.configMapRef.namespace // .spec.opsManager.configMapRef.namespace // ""' <<<"${cr_json}")"
append_ref ConfigMap "${project_cm_ns}" "${project_cm_name}"

# Credentials Secret.
creds_name="$(jq -r '.spec.credentials // ""' <<<"${cr_json}")"
append_ref Secret "${CR_NS}" "${creds_name}"

# TLS / cert prefix.
tls_enabled="$(jq -r '.spec.security.tls.enabled // false' <<<"${cr_json}")"
cert_prefix="$(jq -r '.spec.security.certificatesSecretsPrefix // ""' <<<"${cr_json}")"
if [[ "${tls_enabled}" == "true" || -n "${cert_prefix}" ]]; then
  # Member certs follow the naming pattern <prefix>-<rsName>-cert. We don't
  # know the exact RS names from the CR alone (depends on shard count + role),
  # so we list every Secret matching the prefix.
  if [[ -n "${cert_prefix}" ]]; then
    while IFS= read -r sec; do
      append_ref Secret "${CR_NS}" "${sec}"
    done < <(src_kubectl get secret -n "${CR_NS}" --no-headers -o custom-columns=":metadata.name" 2>/dev/null \
              | grep -E "^${cert_prefix}-" || true)
  fi
  # Agent cert secret (single, named differently): typically <name>-agent-certs
  # — try this best-effort.
  append_ref Secret "${CR_NS}" "${cert_prefix}-agent-certs"
fi

# LDAP / agent secrets.
ldap_bq="$(jq -r '.spec.security.authentication.ldap.bindQuerySecretRef.name // ""' <<<"${cr_json}")"
append_ref Secret "${CR_NS}" "${ldap_bq}"
agent_pw="$(jq -r '.spec.security.authentication.agents.automationPasswordSecretRef.name // ""' <<<"${cr_json}")"
append_ref Secret "${CR_NS}" "${agent_pw}"

# CA bundle CM referenced from inside the project CM (best-effort).
if [[ -n "${project_cm_name}" ]]; then
  ca_cm_name="$(src_kubectl get cm -n "${project_cm_ns:-${CR_NS}}" "${project_cm_name}" -o jsonpath='{.data.sslMMSCAConfigMap}' 2>/dev/null || echo "")"
  if [[ -n "${ca_cm_name}" ]]; then
    append_ref ConfigMap "${project_cm_ns:-${CR_NS}}" "${ca_cm_name}"
  fi
fi

# De-dupe.
sort -u -o "${refs_file}" "${refs_file}"

echo "[replicate] CR ${CR_NS}/${CR_NAME}: refs to replicate:"
sed 's/^/  /' "${refs_file}"
echo

# Copy each ref to every target.
copy_one() {
  local kind="$1" ns="$2" name="$3" target_kc="$4"

  # Fetch source, strip K8s-managed metadata (resourceVersion, uid,
  # selfLink, creationTimestamp, generation, managedFields, ownerReferences).
  local resource_json
  resource_json="$(src_kubectl get "${kind,,}" -n "${ns}" "${name}" -o json 2>/dev/null || echo "")"
  if [[ -z "${resource_json}" ]]; then
    echo "  WARN: ${kind}/${ns}/${name} not found in source — skipping" >&2
    return 0
  fi
  resource_json="$(jq '
    del(.metadata.resourceVersion,
        .metadata.uid,
        .metadata.selfLink,
        .metadata.creationTimestamp,
        .metadata.generation,
        .metadata.managedFields,
        .metadata.ownerReferences,
        .metadata.annotations["kubectl.kubernetes.io/last-applied-configuration"])
  ' <<<"${resource_json}")"

  # Ensure target namespace exists.
  kubectl --kubeconfig "${target_kc}" get ns "${ns}" >/dev/null 2>&1 \
    || kubectl --kubeconfig "${target_kc}" create ns "${ns}" >/dev/null

  # Apply.
  if ! echo "${resource_json}" | kubectl --kubeconfig "${target_kc}" apply -f - >/dev/null; then
    echo "ERROR: apply of ${kind}/${ns}/${name} to $(basename "${target_kc}") failed" >&2
    return 1
  fi
  return 0
}

while IFS= read -r line; do
  IFS='/' read -r kind ns name <<<"${line}"
  for kc in ${TARGET_KUBECONFIGS}; do
    echo "  ${kind}/${ns}/${name} → $(basename "${kc}")"
    copy_one "${kind}" "${ns}" "${name}" "${kc}"
  done
done < "${refs_file}"

echo
echo "[replicate] verifying content-hash agreement across targets..."

# Hash a Secret's or ConfigMap's data canonically. We sort keys, then
# build a "key=base64(value)\n" payload and SHA-256 it. This isn't bit-equal
# to the operator's hashConfigMapData / hashSecretData (those hash a JSON
# structure), but for the purpose of "all targets identical" it's enough —
# if the byte-for-byte data is the same, every target produces the same
# hash, divergence is loud.
hash_data() {
  local kind="$1" ns="$2" name="$3" kc="$4"
  local data_jq
  if [[ "${kind,,}" == "secret" ]]; then
    data_jq='.data // {}'
  else
    data_jq='.data // {}'
  fi
  kubectl --kubeconfig "${kc}" get "${kind,,}" -n "${ns}" "${name}" -o json \
    | jq -r --argjson empty '{}' "
        (${data_jq}) | to_entries | sort_by(.key) |
        map([.key, (.value | tostring)] | @csv) | join(\"\\n\")
      " \
    | sha256sum | awk '{print $1}'
}

mismatch=0
while IFS= read -r line; do
  IFS='/' read -r kind ns name <<<"${line}"
  first_hash=""
  first_kc=""
  for kc in ${TARGET_KUBECONFIGS}; do
    h="$(hash_data "${kind}" "${ns}" "${name}" "${kc}" 2>/dev/null || echo "MISSING")"
    short="${h:0:12}"
    echo "  ${kind}/${ns}/${name}  $(basename "${kc}"): ${short}"
    if [[ -z "${first_hash}" ]]; then
      first_hash="${h}"
      first_kc="$(basename "${kc}")"
    elif [[ "${h}" != "${first_hash}" ]]; then
      echo "    ERROR: hash differs from $(basename "${first_kc}") (${first_hash:0:12} vs ${h:0:12})" >&2
      mismatch=1
    fi
  done
done < "${refs_file}"

if [[ ${mismatch} -ne 0 ]]; then
  echo "[replicate] FAILED: content-hash mismatch across targets — see above." >&2
  exit 1
fi

echo
echo "[replicate] All refs replicated and hashes agree across $(echo "${TARGET_KUBECONFIGS}" | wc -w | tr -d ' ') targets."
