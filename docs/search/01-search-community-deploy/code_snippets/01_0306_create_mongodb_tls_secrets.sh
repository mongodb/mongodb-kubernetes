#!/usr/bin/env bash

set -euo pipefail

: "${MDB_MEMBERS:=3}"

# Inline TLS DNS entry functions (avoiding external file dependency)
mongodb_service_fqdn() {
  printf '%s-svc.%s.svc.cluster.local' "${MDB_RESOURCE_NAME}" "${MDB_NS}"
}

mongodb_wildcard_fqdn() {
  printf '*.%s-svc.%s.svc.cluster.local' "${MDB_RESOURCE_NAME}" "${MDB_NS}"
}

mongodb_dns_entries() {
  local members="${MDB_MEMBERS:-3}"
  local member
  local service
  local wildcard

  service="$(mongodb_service_fqdn)"
  wildcard="$(mongodb_wildcard_fqdn)"

  for ((member = 0; member < members; member++)); do
    printf '%s-%s\n' "${MDB_RESOURCE_NAME}" "${member}"
  done

  for ((member = 0; member < members; member++)); do
    printf '%s-%s.%s\n' "${MDB_RESOURCE_NAME}" "${member}" "${service}"
  done

  printf '%s\n%s\n' "${service}" "${wildcard}"
}

mongot_service_fqdn() {
  printf '%s-search-svc.%s.svc.cluster.local' "${MDB_RESOURCE_NAME}" "${MDB_NS}"
}

mongot_dns_entries() {
  local search_service

  search_service="$(mongot_service_fqdn)"

  printf '%s-search-0\n' "${MDB_RESOURCE_NAME}"
  printf '%s-search-0.%s\n' "${MDB_RESOURCE_NAME}" "${search_service}"
  printf '%s\n' "${search_service}"
}

tls_ca_common_name() {
  mongodb_service_fqdn
}

# Validate OpenSSL is available
if ! command -v openssl &>/dev/null; then
    echo "Error: OpenSSL is required but not installed"
    exit 1
fi

# Mirror the certificate subjects and SAN entries that the optional cert-manager
# helper provisions so users can either self-manage the assets or plug cert-manager
# in to populate the same Kubernetes secrets automatically.

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

# Pre-calculate all DNS entries and services
mongodb_service="$(mongodb_service_fqdn)"
mongodb_sans=()
while IFS= read -r entry; do
  mongodb_sans+=("${entry}")
done < <(mongodb_dns_entries)
mongodb_san=$(printf 'DNS:%s,' "${mongodb_sans[@]}")
mongodb_san="${mongodb_san%,}"

mongot_service="$(mongot_service_fqdn)"
mongot_sans=()
while IFS= read -r entry; do
  mongot_sans+=("${entry}")
done < <(mongot_dns_entries)
mongot_san=$(printf 'DNS:%s,' "${mongot_sans[@]}")
mongot_san="${mongot_san%,}"

# Function to handle OpenSSL errors
openssl_exec() {
    if ! "$@" >/dev/null 2>&1; then
        echo "Error: OpenSSL command failed: $*" >&2
        exit 1
    fi
}

# Create CA certificate
echo "Generating CA certificate..."
openssl_exec openssl genrsa -out "${tmpdir}/ca.key" 2048
openssl_exec openssl req -x509 -new -key "${tmpdir}/ca.key" \
  -out "${tmpdir}/ca.crt" \
  -days 365 \
  -sha256 \
  -subj "/CN=$(tls_ca_common_name)" \
  -addext "basicConstraints = critical,CA:true" \
  -addext "keyUsage = critical,keyCertSign,cRLSign" \
  -addext "subjectKeyIdentifier = hash"

# Optimized function to sign certificates
sign_cert() {
    local name="$1"
    local common_name="$2"
    local san="$3"

    echo "Generating certificate for ${name}..."

    # Generate private key
    openssl_exec openssl genrsa -out "${tmpdir}/${name}.key" 2048

    # Create CSR
    openssl_exec openssl req -new \
        -key "${tmpdir}/${name}.key" \
        -out "${tmpdir}/${name}.csr" \
        -subj "/CN=${common_name}" \
        -addext "subjectAltName = ${san}" \
        -addext "extendedKeyUsage = serverAuth,clientAuth" \
        -addext "keyUsage = digitalSignature,keyEncipherment"

    # Sign certificate with consolidated extensions including client auth
    openssl_exec openssl x509 -req -in "${tmpdir}/${name}.csr" \
        -CA "${tmpdir}/ca.crt" \
        -CAkey "${tmpdir}/ca.key" \
        -CAcreateserial \
        -out "${tmpdir}/${name}.crt" \
        -days 365 \
        -sha256 \
        -extfile <(cat <<EOF
subjectAltName=${san}
extendedKeyUsage=serverAuth,clientAuth
keyUsage=digitalSignature,keyEncipherment
basicConstraints=CA:FALSE
EOF
)
}

# Generate certificates for MongoDB and Search
sign_cert mongodb "${mongodb_service}" "${mongodb_san}"
sign_cert mongot "${mongot_service}" "${mongot_san}"

# Function to create secrets with consistent pattern
create_tls_secret() {
    local secret_name="$1"
    local cert_file="$2"
    local key_file="$3"
    local secret_type="${4:-tls}"

    echo "Creating secret ${secret_name}..."
    if [[ "${secret_type}" == "generic" ]]; then
        kubectl create secret generic "${secret_name}" \
            --from-file=ca.crt="${cert_file}" \
            --dry-run=client -o yaml \
            | kubectl apply --context "${K8S_CTX}" --namespace "${MDB_NS}" -f -
    else
        kubectl create secret tls "${secret_name}" \
            --cert="${cert_file}" \
            --key="${key_file}" \
            --dry-run=client -o yaml \
            | kubectl apply --context "${K8S_CTX}" --namespace "${MDB_NS}" -f -
    fi
}

# Create all secrets
echo "Creating Kubernetes secrets..."
create_tls_secret "${MDB_TLS_CA_SECRET_NAME}" "${tmpdir}/ca.crt" "" "generic"
create_tls_secret "${MDB_TLS_SERVER_CERT_SECRET_NAME}" "${tmpdir}/mongodb.crt" "${tmpdir}/mongodb.key"
create_tls_secret "${MDB_SEARCH_TLS_SECRET_NAME}" "${tmpdir}/mongot.crt" "${tmpdir}/mongot.key"

echo "TLS certificates and secrets created successfully"
