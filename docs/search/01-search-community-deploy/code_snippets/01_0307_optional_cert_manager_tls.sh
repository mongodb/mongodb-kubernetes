#!/usr/bin/env bash

set -euo pipefail

# Self-contained cert-manager TLS certificate management
# This script automatically installs cert-manager if not present and creates TLS certificates

echo "Starting cert-manager TLS certificate setup..."

# Function to check for required environment variables
check_required_vars() {
    local required_vars=("MDB_RESOURCE_NAME" "MDB_NS" "K8S_CTX" "MDB_TLS_CA_SECRET_NAME" "MDB_TLS_SERVER_CERT_SECRET_NAME" "MDB_SEARCH_TLS_SECRET_NAME")
    local missing_vars=()

    for var in "${required_vars[@]}"; do
        if [[ -z "${!var:-}" ]]; then
            missing_vars+=("${var}")
        fi
    done

    if [[ ${#missing_vars[@]} -gt 0 ]]; then
        echo "ERROR: Missing required environment variables: ${missing_vars[*]}"
        echo "Please set these variables before running the script."
        exit 1
    fi
}

# Function to force clean cert-manager resources with proper ordering
force_cleanup_cert_manager_resources() {
    echo "Force cleaning up cert-manager resources to prevent conflicts..."

    # Force delete certificates (remove finalizers if stuck)
    for cert in "${MDB_SEARCH_TLS_SECRET_NAME}" "${MDB_TLS_SERVER_CERT_SECRET_NAME}" "${MDB_TLS_CA_SECRET_NAME}"; do
        if kubectl get certificate "${cert}" --context "${K8S_CTX}" -n "${MDB_NS}" >/dev/null 2>&1; then
            echo "Force deleting certificate ${cert}..."
            kubectl patch certificate "${cert}" --context "${K8S_CTX}" -n "${MDB_NS}" -p '{"metadata":{"finalizers":null}}' --type=merge || true
            kubectl delete certificate "${cert}" --context "${K8S_CTX}" -n "${MDB_NS}" --ignore-not-found=true --force --grace-period=0 || true
        fi
    done

    # Force delete issuers
    for issuer in "${MDB_TLS_CA_ISSUER}" "${MDB_TLS_SELF_SIGNED_ISSUER}"; do
        if kubectl get issuer "${issuer}" --context "${K8S_CTX}" -n "${MDB_NS}" >/dev/null 2>&1; then
            echo "Force deleting issuer ${issuer}..."
            kubectl patch issuer "${issuer}" --context "${K8S_CTX}" -n "${MDB_NS}" -p '{"metadata":{"finalizers":null}}' --type=merge || true
            kubectl delete issuer "${issuer}" --context "${K8S_CTX}" -n "${MDB_NS}" --ignore-not-found=true --force --grace-period=0 || true
        fi
    done

    # Delete related secrets if they exist in bad state
    for secret in "${MDB_SEARCH_TLS_SECRET_NAME}" "${MDB_TLS_SERVER_CERT_SECRET_NAME}" "${MDB_TLS_CA_SECRET_NAME}"; do
        if kubectl get secret "${secret}" --context "${K8S_CTX}" -n "${MDB_NS}" >/dev/null 2>&1; then
            echo "Cleaning up secret ${secret}..."
            kubectl delete secret "${secret}" --context "${K8S_CTX}" -n "${MDB_NS}" --ignore-not-found=true
        fi
    done

    echo "Waiting for cleanup to complete..."
    sleep 10

    # Verify cleanup completed
    local cleanup_failed=false
    for resource in "${MDB_SEARCH_TLS_SECRET_NAME}" "${MDB_TLS_SERVER_CERT_SECRET_NAME}" "${MDB_TLS_CA_SECRET_NAME}"; do
        if kubectl get certificate "${resource}" --context "${K8S_CTX}" -n "${MDB_NS}" >/dev/null 2>&1; then
            echo "WARNING: Certificate ${resource} still exists after cleanup"
            cleanup_failed=true
        fi
    done

    if [[ "${cleanup_failed}" == "true" ]]; then
        echo "Cleanup incomplete, but proceeding with creation..."
    fi
}

# Function to install cert-manager with enhanced configuration
install_cert_manager() {
    echo "Installing cert-manager with enhanced configuration..."

    # Install cert-manager CRDs
    kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.13.2/cert-manager.crds.yaml

    # Add Jetstack Helm repository
    helm repo add jetstack https://charts.jetstack.io || true
    helm repo update

    # Install cert-manager using Helm with enhanced settings
    helm install cert-manager jetstack/cert-manager \
        --namespace cert-manager \
        --create-namespace \
        --version v1.13.2 \
        --set installCRDs=false \
        --set global.leaderElection.namespace=cert-manager \
        --set webhook.timeoutSeconds=30 \
        --set cainjector.extraArgs[0]="--leader-elect=true" \
        --set cainjector.extraArgs[1]="--leader-election-lease-duration=60s" \
        --set cainjector.extraArgs[2]="--leader-election-renew-deadline=40s" \
        --set cainjector.extraArgs[3]="--leader-election-retry-period=15s"

    echo "Waiting for cert-manager to be ready..."
    kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=cert-manager -n cert-manager --timeout=300s
    kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=cainjector -n cert-manager --timeout=300s
    kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=webhook -n cert-manager --timeout=300s

    # Verify cert-manager webhook is accessible
    echo "Verifying cert-manager webhook..."
    kubectl get validatingwebhookconfiguration cert-manager-webhook
    kubectl get mutatingwebhookconfiguration cert-manager-webhook
}

# Enhanced cert-manager readiness check
verify_cert_manager_ready() {
    echo "Verifying cert-manager is fully operational..."

    # Check if cert-manager pods are ready
    if ! kubectl get pods -n cert-manager -l app.kubernetes.io/name=cert-manager --no-headers | grep -q "1/1.*Running"; then
        echo "cert-manager pods not ready, installing..."
        install_cert_manager
        return
    fi

    # Test webhook connectivity
    if ! kubectl get validatingwebhookconfiguration cert-manager-webhook >/dev/null 2>&1; then
        echo "cert-manager webhook not found, reinstalling..."
        install_cert_manager
        return
    fi

    echo "cert-manager is ready"
}

# Check if cert-manager is available, install if not
if ! kubectl get crd certificates.cert-manager.io &>/dev/null; then
    echo "cert-manager CRDs not found. Installing cert-manager..."
    install_cert_manager
else
    echo "cert-manager CRDs found. Verifying cert-manager readiness..."
    verify_cert_manager_ready
fi

# Check required environment variables
check_required_vars

: "${MDB_MEMBERS:=3}"
: "${MDB_TLS_SELF_SIGNED_ISSUER:=${MDB_RESOURCE_NAME}-selfsigned-issuer}"
: "${MDB_TLS_CA_ISSUER:=${MDB_RESOURCE_NAME}-ca-issuer}"

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
    printf '%s-%s.%s\n' "${MDB_RESOURCE_NAME}" "${member}" "${service}"
  done

  printf '%s\n%s\n' "${service}" "${wildcard}"
}

mongot_service_fqdn() {
  printf '%s-search-svc.%s.svc.cluster.local' "${MDB_RESOURCE_NAME}" "${MDB_NS}"
}

mongot_wildcard_fqdn() {
  printf '*.%s-search.%s.svc.cluster.local' "${MDB_RESOURCE_NAME}" "${MDB_NS}"
}

mongot_dns_entries() {
  mongot_service_fqdn
  printf '\n'
  mongot_wildcard_fqdn
  printf '\n'
  # Add individual search pod hostname for proper TLS validation
  printf '%s-search-0.%s-search-svc.%s.svc.cluster.local\n' "${MDB_RESOURCE_NAME}" "${MDB_NS}"
  # Add localhost and IP for local connections
  printf 'localhost\n'
  printf '127.0.0.1\n'
}

tls_ca_common_name() {
  mongodb_service_fqdn
}

dns_entries_yaml_block() {
  local indent="${1:-    }"
  local entry

  while IFS= read -r entry; do
    printf '%s- "%s"\n' "${indent}" "${entry}"
  done
}

ca_common_name="$(tls_ca_common_name)"
mongodb_service="$(mongodb_service_fqdn)"
mongodb_dns_block="$(mongodb_dns_entries | dns_entries_yaml_block '    ')"
mongot_service="$(mongot_service_fqdn)"
mongot_dns_block="$(mongot_dns_entries | dns_entries_yaml_block '    ')"

# Enhanced function to wait for issuer with better error handling
wait_for_issuer() {
    local issuer_name="$1"
    local max_retries=20
    local retry_count=0

    echo "Waiting for issuer ${issuer_name} to be ready..."

    while [ ${retry_count} -lt ${max_retries} ]; do
        local ready_status
        ready_status=$(kubectl get issuer "${issuer_name}" --context "${K8S_CTX}" -n "${MDB_NS}" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "")

        if [[ "${ready_status}" == "True" ]]; then
            echo "✓ Issuer ${issuer_name} is ready"
            return 0
        fi

        retry_count=$((retry_count + 1))
        echo "Issuer ${issuer_name} not ready (status: ${ready_status:-Unknown}), attempt ${retry_count}/${max_retries}"

        if [ ${retry_count} -eq 5 ] || [ ${retry_count} -eq 10 ] || [ ${retry_count} -eq 15 ]; then
            echo "Checking issuer status details..."
            kubectl describe issuer "${issuer_name}" --context "${K8S_CTX}" -n "${MDB_NS}" || true
            echo "Checking cert-manager controller logs..."
            kubectl logs -n cert-manager -l app.kubernetes.io/name=cert-manager --tail=10 || true
        fi

        if [ ${retry_count} -lt ${max_retries} ]; then
            sleep 10
        fi
    done

    echo "ERROR: Issuer ${issuer_name} failed to become ready after ${max_retries} attempts"
    kubectl describe issuer "${issuer_name}" --context "${K8S_CTX}" -n "${MDB_NS}" || true
    kubectl get events --context "${K8S_CTX}" -n "${MDB_NS}" --sort-by='.lastTimestamp' | grep -i issuer || true
    exit 1
}

# Enhanced certificate waiting with conflict resolution
wait_for_certificate() {
  local certificate_name="$1"
  local max_retries=20
  local retry_count=0

  echo "Waiting for certificate ${certificate_name} to be ready..."

  while [ ${retry_count} -lt ${max_retries} ]; do
    local ready_status
    ready_status=$(kubectl get certificate "${certificate_name}" --context "${K8S_CTX}" -n "${MDB_NS}" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "")

    if [[ "${ready_status}" == "True" ]]; then
      echo "✓ Certificate ${certificate_name} is ready"

      # Verify the secret exists and has required fields
      if kubectl get secret "${certificate_name}" --context "${K8S_CTX}" -n "${MDB_NS}" >/dev/null 2>&1; then
        local tls_crt tls_key
        tls_crt=$(kubectl get secret "${certificate_name}" --context "${K8S_CTX}" -n "${MDB_NS}" -o jsonpath='{.data.tls\.crt}' 2>/dev/null || true)
        tls_key=$(kubectl get secret "${certificate_name}" --context "${K8S_CTX}" -n "${MDB_NS}" -o jsonpath='{.data.tls\.key}' 2>/dev/null || true)

        if [[ -n "${tls_crt}" && -n "${tls_key}" ]]; then
          echo "✓ Certificate secret ${certificate_name} contains required tls.crt and tls.key"
          return 0
        else
          echo "⚠ Certificate secret ${certificate_name} missing tls.crt or tls.key fields"
        fi
      else
        echo "⚠ Certificate secret ${certificate_name} not found"
      fi
    fi

    retry_count=$((retry_count + 1))
    echo "Certificate ${certificate_name} not ready (status: ${ready_status:-Unknown}), attempt ${retry_count}/${max_retries}"

    # Check for common issues and provide diagnostics
    if [ ${retry_count} -eq 5 ] || [ ${retry_count} -eq 10 ] || [ ${retry_count} -eq 15 ]; then
      echo "Checking certificate status details..."
      kubectl describe certificate "${certificate_name}" --context "${K8S_CTX}" -n "${MDB_NS}" || true

      echo "Checking for certificate request issues..."
      kubectl get certificaterequests --context "${K8S_CTX}" -n "${MDB_NS}" | grep "${certificate_name}" || true

      echo "Checking cert-manager controller logs..."
      kubectl logs -n cert-manager -l app.kubernetes.io/name=cert-manager --tail=20 | grep -i "${certificate_name}" || true

      # Check for resource conflicts and try to resolve
      local conflict_found
      conflict_found=$(kubectl describe certificate "${certificate_name}" --context "${K8S_CTX}" -n "${MDB_NS}" 2>/dev/null | grep -i "object has been modified" || true)
      if [[ -n "${conflict_found}" ]]; then
        echo "Detected resource conflict, attempting to resolve..."
        kubectl annotate certificate "${certificate_name}" --context "${K8S_CTX}" -n "${MDB_NS}" cert-manager.io/force-renew="$(date +%s)" --overwrite || true
        sleep 5
      fi
    fi

    if [ ${retry_count} -lt ${max_retries} ]; then
      sleep 15
    fi
  done

  echo "ERROR: Certificate ${certificate_name} failed to become ready after ${max_retries} attempts"
  kubectl describe certificate "${certificate_name}" --context "${K8S_CTX}" -n "${MDB_NS}" || true
  kubectl get events --context "${K8S_CTX}" -n "${MDB_NS}" --sort-by='.lastTimestamp' | grep -i certificate || true
  exit 1
}

# Enhanced CA secret verification with proper SSL format
ensure_ca_secret_has_ca_crt() {
  local secret_name="$1"

  echo "Ensuring CA secret ${secret_name} has proper ca.crt field..."

  # Check if ca.crt field already exists
  local existing_ca
  existing_ca=$(kubectl get secret "${secret_name}" --context "${K8S_CTX}" --namespace "${MDB_NS}" \
      -o jsonpath='{.data.ca\.crt}' 2>/dev/null || true)

  if [[ -n "${existing_ca}" ]]; then
    echo "✓ CA secret ${secret_name} already has ca.crt field"
    return
  fi

  local tls_crt
  tls_crt=$(kubectl get secret "${secret_name}" \
    --context "${K8S_CTX}" \
    --namespace "${MDB_NS}" \
    -o jsonpath='{.data.tls\.crt}' 2>/dev/null || true)

  if [[ -z "${tls_crt}" ]]; then
    echo "ERROR: Failed to retrieve tls.crt from secret ${secret_name}" >&2
    kubectl describe secret "${secret_name}" --context "${K8S_CTX}" -n "${MDB_NS}" || true
    exit 1
  fi

  echo "Adding ca.crt field to CA secret ${secret_name}..."
  kubectl patch secret "${secret_name}" \
    --context "${K8S_CTX}" \
    --namespace "${MDB_NS}" \
    -p "{\"data\":{\"ca.crt\":\"${tls_crt}\"}}"

  echo "✓ CA secret ${secret_name} updated with ca.crt field"
}

# Force cleanup existing resources to prevent conflicts
force_cleanup_cert_manager_resources

# Step 1: Create self-signed issuer with retry logic
echo "Step 1: Creating self-signed issuer..."
attempt=0
max_attempts=3
while [ ${attempt} -lt ${max_attempts} ]; do
    if kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_SELF_SIGNED_ISSUER
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: ${MDB_TLS_SELF_SIGNED_ISSUER}
  namespace: ${MDB_NS}
  annotations:
    cert-manager.io/revision: "$(date +%s)"
spec:
  selfSigned: {}
EOF_SELF_SIGNED_ISSUER
    then
        break
    else
        attempt=$((attempt + 1))
        echo "Failed to create self-signed issuer, attempt ${attempt}/${max_attempts}"
        if [ ${attempt} -lt ${max_attempts} ]; then
            sleep 5
        else
            echo "ERROR: Failed to create self-signed issuer after ${max_attempts} attempts"
            exit 1
        fi
    fi
done

wait_for_issuer "${MDB_TLS_SELF_SIGNED_ISSUER}"

# Step 2: Create CA certificate matching the test configuration
echo "Step 2: Creating CA certificate..."
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_CA_CERT
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_TLS_CA_SECRET_NAME}
  namespace: ${MDB_NS}
  annotations:
    cert-manager.io/revision: "$(date +%s)"
spec:
  isCA: true
  secretName: ${MDB_TLS_CA_SECRET_NAME}
  commonName: "${ca_common_name}"
  duration: 240h
  renewBefore: 120h
  privateKey:
    algorithm: ECDSA
    size: 256
  usages:
    - digital signature
    - key encipherment
    - cert sign
  issuerRef:
    name: ${MDB_TLS_SELF_SIGNED_ISSUER}
    kind: Issuer
EOF_CA_CERT

wait_for_certificate "${MDB_TLS_CA_SECRET_NAME}"
ensure_ca_secret_has_ca_crt "${MDB_TLS_CA_SECRET_NAME}"

# Step 3: Create CA issuer
echo "Step 3: Creating CA issuer..."
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_CA_ISSUER
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: ${MDB_TLS_CA_ISSUER}
  namespace: ${MDB_NS}
  annotations:
    cert-manager.io/revision: "$(date +%s)"
spec:
  ca:
    secretName: ${MDB_TLS_CA_SECRET_NAME}
EOF_CA_ISSUER

wait_for_issuer "${MDB_TLS_CA_ISSUER}"

# Step 4: Create MongoDB certificate matching the test format
echo "Step 4: Creating MongoDB certificate..."
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_MONGODB_CERT
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_TLS_SERVER_CERT_SECRET_NAME}
  namespace: ${MDB_NS}
  annotations:
    cert-manager.io/revision: "$(date +%s)"
spec:
  secretName: ${MDB_TLS_SERVER_CERT_SECRET_NAME}
  duration: 240h
  renewBefore: 120h
  usages:
    - server auth
    - client auth
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: Issuer
  commonName: "${mongodb_service}"
  dnsNames:
${mongodb_dns_block}
EOF_MONGODB_CERT

# Step 5: Create MongoDB Search certificate matching the test format
echo "Step 5: Creating MongoDB Search certificate..."
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_SEARCH_CERT
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_SEARCH_TLS_SECRET_NAME}
  namespace: ${MDB_NS}
  annotations:
    cert-manager.io/revision: "$(date +%s)"
spec:
  secretName: ${MDB_SEARCH_TLS_SECRET_NAME}
  duration: 240h
  renewBefore: 120h
  usages:
    - server auth
    - client auth
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: Issuer
  commonName: "${mongot_service}"
  dnsNames:
${mongot_dns_block}
    - "${MDB_RESOURCE_NAME}-search-svc.${MDB_NS}.svc.cluster.local"
EOF_SEARCH_CERT

# Wait for all certificates with enhanced monitoring
echo "Waiting for MongoDB certificates to be issued..."
wait_for_certificate "${MDB_TLS_SERVER_CERT_SECRET_NAME}"
wait_for_certificate "${MDB_SEARCH_TLS_SECRET_NAME}"

echo "All TLS certificates have been successfully created by cert-manager"
echo "Performing final verification..."

# Enhanced verification with SSL certificate details
for secret in "${MDB_TLS_CA_SECRET_NAME}" "${MDB_TLS_SERVER_CERT_SECRET_NAME}" "${MDB_SEARCH_TLS_SECRET_NAME}"; do
    if kubectl get secret "${secret}" --context "${K8S_CTX}" -n "${MDB_NS}" >/dev/null 2>&1; then
        echo "✓ Secret ${secret} exists"

        # Verify certificate details
        cert_data=$(kubectl get secret "${secret}" --context "${K8S_CTX}" -n "${MDB_NS}" -o jsonpath='{.data.tls\.crt}' | base64 -d)
        if echo "${cert_data}" | openssl x509 -noout -text >/dev/null 2>&1; then
            subject=$(echo "${cert_data}" | openssl x509 -noout -subject | sed 's/subject=//')
            echo "  ✓ Valid X.509 certificate: ${subject}"
        else
            echo "  ⚠ Invalid certificate format in ${secret}"
        fi
    else
        echo "✗ Secret ${secret} is missing"
        exit 1
    fi
done

# Create a status ConfigMap for other components to check
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_STATUS
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${MDB_RESOURCE_NAME}-tls-status
  namespace: ${MDB_NS}
data:
  status: "ready"
  ca-secret: "${MDB_TLS_CA_SECRET_NAME}"
  mongodb-secret: "${MDB_TLS_SERVER_CERT_SECRET_NAME}"
  search-secret: "${MDB_SEARCH_TLS_SECRET_NAME}"
  created: "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
EOF_STATUS

echo "TLS certificate setup completed successfully"
echo "Status ConfigMap created: ${MDB_RESOURCE_NAME}-tls-status"
