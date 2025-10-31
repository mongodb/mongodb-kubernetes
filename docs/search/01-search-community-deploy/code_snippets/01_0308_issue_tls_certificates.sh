#!/usr/bin/env bash

set -euo pipefail

required=(
  K8S_CTX
  MDB_NS
  MDB_RESOURCE_NAME
  MDB_MEMBERS
  MDB_TLS_CA_SECRET_NAME
  MDB_TLS_SERVER_CERT_SECRET_NAME
  MDB_SEARCH_TLS_SECRET_NAME
)
missing=()
for var in "${required[@]}"; do
  [[ -n "${!var:-}" ]] || missing+=("${var}")
done
if (( ${#missing[@]} )); then
  echo "Missing required environment variables: ${missing[*]}" >&2
  exit 1
fi

ca_issuer="${MDB_RESOURCE_NAME}-ca-issuer"
server_certificate="${MDB_RESOURCE_NAME}-server-tls"
search_certificate="${MDB_RESOURCE_NAME}-search-tls"

mongo_dns_names=()
for ((member = 0; member < ${MDB_MEMBERS}; member++)); do
  mongo_dns_names+=("${MDB_RESOURCE_NAME}-${member}")
  mongo_dns_names+=("${MDB_RESOURCE_NAME}-${member}.${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local")
done
mongo_dns_names+=(
  "${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local"
  "*.${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local"
)

search_dns_names=(
  "${MDB_RESOURCE_NAME}-search-0"
  "${MDB_RESOURCE_NAME}-search-0.${MDB_RESOURCE_NAME}-search-svc.${MDB_NS}.svc.cluster.local"
  "${MDB_RESOURCE_NAME}-search-svc.${MDB_NS}.svc.cluster.local"
)

render_dns_list() {
  local dns_list=("$@")
  for dns in "${dns_list[@]}"; do
    printf "      - \"%s\"\n" "${dns}"
  done
}

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${server_certificate}
  namespace: ${MDB_NS}
spec:
  secretName: ${MDB_TLS_SERVER_CERT_SECRET_NAME}
  issuerRef:
    kind: Issuer
    name: ${ca_issuer}
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
    kind: Issuer
    name: ${ca_issuer}
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

echo "MongoDB TLS certificates have been issued."
