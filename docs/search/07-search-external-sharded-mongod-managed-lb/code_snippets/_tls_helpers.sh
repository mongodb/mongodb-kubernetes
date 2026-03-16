#!/usr/bin/env bash
# Shared TLS helper functions for cert-manager Certificate creation
#
# DNS naming pattern: <pod>-<ordinal>.<headless-svc>.<namespace>.svc.cluster.local
# Example: ext-mdb-sh-0-0.ext-mdb-sh-sh.mongodb.svc.cluster.local

# Build YAML dns list from a count, pod prefix, and headless service name.
# Usage: build_dns_names <count> <pod-prefix> <service-name>
# Example: build_dns_names 3 "ext-mdb-sh-0" "ext-mdb-sh-sh"
build_dns_names() {
  local count="$1" pod_prefix="$2" svc="$3"
  local dns=""
  for ((i = 0; i < count; i++)); do
    dns="${dns}    - ${pod_prefix}-${i}.${svc}.${MDB_NS}.svc.cluster.local
"
  done
  dns="${dns}    - \"*.${svc}.${MDB_NS}.svc.cluster.local\""
  echo "${dns}"
}

# Apply a cert-manager Certificate CR.
# Usage: create_cert <name> <dns_names_yaml> [usages_yaml]
# Default usages: server auth + client auth
create_cert() {
  local name="$1" dns_names="$2"
  local usages="${3:-"    - server auth
    - client auth"}"

  kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${name}
spec:
  secretName: ${name}
  duration: 8760h    # 1 year
  renewBefore: 720h  # 30 days
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
${usages}
  dnsNames:
${dns_names}
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF
}
