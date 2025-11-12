#!/usr/bin/env bash

set -euo pipefail

required_env=(
  "K8S_CTX"
  "MDB_NS"
  "CERT_MANAGER_NAMESPACE"
  "MDB_RESOURCE_NAME"
  "MDB_TLS_SELF_SIGNED_ISSUER"
  "MDB_TLS_CA_CERT_NAME"
  "MDB_TLS_CA_SECRET_NAME"
  "MDB_TLS_CA_CONFIGMAP"
  "MDB_TLS_CA_ISSUER"
  "MDB_TLS_SERVER_CERT_SECRET_NAME"
  "MDB_SEARCH_TLS_SECRET_NAME"
  "MDB_SEARCH_SERVICE_NAME"
  "MDB_SEARCH_HOSTNAME"
)

for var in "${required_env[@]}"; do
  if [[ -z "${!var:-}" ]]; then
    echo "Environment variable ${var} must be set" >&2
    exit 1
  fi
done

server_certificate="${MDB_RESOURCE_NAME}-server-tls"
search_certificate="${MDB_RESOURCE_NAME}-search-tls"

kubectl apply --context "${K8S_CTX}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${MDB_TLS_SELF_SIGNED_ISSUER}
spec:
  selfSigned: {}
EOF_MANIFEST

kubectl --context "${K8S_CTX}" wait --for=condition=Ready clusterissuer "${MDB_TLS_SELF_SIGNED_ISSUER}" --timeout=120s

kubectl apply --context "${K8S_CTX}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_TLS_CA_CERT_NAME}
  namespace: ${CERT_MANAGER_NAMESPACE}
spec:
  isCA: true
  commonName: ${MDB_TLS_CA_CERT_NAME}
  secretName: ${MDB_TLS_CA_SECRET_NAME}
  privateKey:
    algorithm: ECDSA
    size: 256
  issuerRef:
    name: ${MDB_TLS_SELF_SIGNED_ISSUER}
    kind: ClusterIssuer
EOF_MANIFEST

kubectl --context "${K8S_CTX}" wait --for=condition=Ready -n "${CERT_MANAGER_NAMESPACE}" certificate "${MDB_TLS_CA_CERT_NAME}" --timeout=300s

kubectl apply --context "${K8S_CTX}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${MDB_TLS_CA_ISSUER}
spec:
  ca:
    secretName: ${MDB_TLS_CA_SECRET_NAME}
EOF_MANIFEST

kubectl --context "${K8S_CTX}" wait --for=condition=Ready clusterissuer "${MDB_TLS_CA_ISSUER}" --timeout=120s

tmp_ca_cert="$(mktemp)"
trap 'rm -f "${tmp_ca_cert}"' EXIT

# Extract CA certificate data (prefer ca.crt, fallback to tls.crt)
ca_data=$(kubectl --context "${K8S_CTX}" get secret "${MDB_TLS_CA_SECRET_NAME}" -n "${CERT_MANAGER_NAMESPACE}" -o jsonpath="{.data['ca\\.crt']}") || true
if [[ -z "${ca_data}" ]]; then
  ca_data=$(kubectl --context "${K8S_CTX}" get secret "${MDB_TLS_CA_SECRET_NAME}" -n "${CERT_MANAGER_NAMESPACE}" -o jsonpath="{.data['tls\\.crt']}") || true
fi
if [[ -z "${ca_data}" ]]; then
  echo "Failed to retrieve CA certificate data from secret ${MDB_TLS_CA_SECRET_NAME} in namespace ${CERT_MANAGER_NAMESPACE}" >&2
  exit 1
fi

echo "CA certificate data retrieved for secret ${MDB_TLS_CA_SECRET_NAME}."

# Write CA cert to temp file
printf '%s' "${ca_data}" | base64 --decode > "${tmp_ca_cert}"

# Create namespaced CA secret with multiple key variants for compatibility
kubectl --context "${K8S_CTX}" create secret generic "${MDB_TLS_CA_SECRET_NAME}" -n "${MDB_NS}" \
  --from-file=ca.crt="${tmp_ca_cert}" \
  --from-file=ca-pem="${tmp_ca_cert}" \
  --from-file=mms-ca.crt="${tmp_ca_cert}" \
  --dry-run=client -o yaml | kubectl --context "${K8S_CTX}" apply -f -

add_unique_dns() {
  local -n seen_ref=$1
  local -n collection_ref=$2
  local candidate=$3
  [[ -z "${candidate}" ]] && return 0
  if [[ -z "${seen_ref[${candidate}]:-}" ]]; then
    seen_ref["${candidate}"]=1
    collection_ref+=("${candidate}")
  fi
}

render_dns_list() {
  local entries=("$@")
  for entry in "${entries[@]}"; do
    printf "      - \"%s\"\n" "${entry}"
  done
}

declare -A mongo_seen=()
mongo_dns_names=()

for host_var in MDB_EXTERNAL_HOST_0 MDB_EXTERNAL_HOST_1 MDB_EXTERNAL_HOST_2; do
  host_value="${!host_var:-}"
  if [[ -n "${host_value}" ]]; then
    add_unique_dns mongo_seen mongo_dns_names "${host_value%%:*}"
  fi
done

add_unique_dns mongo_seen mongo_dns_names "${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local"
add_unique_dns mongo_seen mongo_dns_names "*.${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local"

if [[ ${#mongo_dns_names[@]} -eq 0 ]]; then
  echo "No MongoDB DNS names generated; ensure MDB_EXTERNAL_HOST_* variables are set" >&2
  exit 1
fi

declare -A search_seen=()
search_dns_names=()

add_unique_dns search_seen search_dns_names "${MDB_SEARCH_SERVICE_NAME}"
add_unique_dns search_seen search_dns_names "${MDB_SEARCH_SERVICE_NAME}.${MDB_NS}.svc.cluster.local"
add_unique_dns search_seen search_dns_names "${MDB_SEARCH_SERVICE_NAME}-search-svc.${MDB_NS}.svc.cluster.local"
add_unique_dns search_seen search_dns_names "*.${MDB_SEARCH_SERVICE_NAME}-search-svc.${MDB_NS}.svc.cluster.local"
add_unique_dns search_seen search_dns_names "${MDB_SEARCH_HOSTNAME}"

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${server_certificate}
  namespace: ${MDB_NS}
spec:
  secretName: ${MDB_TLS_SERVER_CERT_SECRET_NAME}
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
  duration: 240h0m0s
  renewBefore: 120h0m0s
  usages:
    - digital signature
    - key encipherment
    - server auth
    - client auth
  dnsNames:
$(render_dns_list "${mongo_dns_names[@]}")
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${search_certificate}
  namespace: ${MDB_NS}
spec:
  secretName: ${MDB_SEARCH_TLS_SECRET_NAME}
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
  duration: 240h0m0s
  renewBefore: 120h0m0s
  usages:
    - digital signature
    - key encipherment
    - server auth
    - client auth
  dnsNames:
$(render_dns_list "${search_dns_names[@]}")
EOF_MANIFEST

kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready certificate "${server_certificate}" --timeout=300s
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready certificate "${search_certificate}" --timeout=300s

echo "TLS assets have been issued via cert-manager and stored in Kubernetes secrets."
