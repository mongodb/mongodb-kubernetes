#!/usr/bin/env bash
set -euo pipefail

# Always provision cert-manager TLS assets in a fresh environment.
# Installs cert-manager, waits for webhook readiness, then creates:
#  - Self-signed Issuer
#  - CA Certificate (secret)
#  - CA Issuer
#  - Server & Search Certificates
#  - CA ConfigMap (optional consumer)

: "${CERT_MANAGER_NAMESPACE:=cert-manager}"

required=(K8S_CTX MDB_NS MDB_RESOURCE_NAME MDB_TLS_CA_SECRET_NAME MDB_TLS_SERVER_CERT_SECRET_NAME MDB_SEARCH_TLS_SECRET_NAME MDB_TLS_CA_CONFIGMAP_NAME)
missing=()
for v in "${required[@]}"; do [[ -z "${!v:-}" ]] && missing+=("$v"); done
if (( ${#missing[@]} )); then
  echo "Missing required env vars: ${missing[*]}" >&2; exit 1; fi

install_cert_manager() {
  echo "Installing cert-manager..."
  helm repo add jetstack https://charts.jetstack.io --force-update >/dev/null 2>&1 || true
  helm upgrade --install cert-manager jetstack/cert-manager \
    --kube-context "${K8S_CTX}" \
    --namespace "${CERT_MANAGER_NAMESPACE}" \
    --create-namespace \
    --set crds.enabled=true 1>/dev/null

  echo "Waiting for cert-manager deployments to be Available..."
  for dep in cert-manager cert-manager-cainjector cert-manager-webhook; do
    kubectl --context "${K8S_CTX}" wait -n "${CERT_MANAGER_NAMESPACE}" --for=condition=Available deployment/${dep} --timeout=300s || {
      echo "ERROR: deployment ${dep} not Available" >&2; exit 1; }
  done

  echo "Waiting for webhook service existence..."
  local tries=0 max_tries=30
  until kubectl --context "${K8S_CTX}" get svc cert-manager-webhook -n "${CERT_MANAGER_NAMESPACE}" >/dev/null 2>&1; do
    ((tries++)); [[ $tries -ge $max_tries ]] && { echo "ERROR: cert-manager-webhook service not found" >&2; exit 1; }
    sleep 5
  done

  echo "Waiting for webhook endpoints to have at least one address..."
  tries=0
  until [[ $(kubectl --context "${K8S_CTX}" get endpoints cert-manager-webhook -n "${CERT_MANAGER_NAMESPACE}" -o jsonpath='{.subsets[0].addresses[0].ip}' 2>/dev/null || echo '') != '' ]]; do
    ((tries++)); [[ $tries -ge $max_tries ]] && { echo "ERROR: webhook endpoints have no addresses" >&2; exit 1; }
    sleep 5
  done
  echo "cert-manager webhook ready."
}

install_cert_manager

SELF_ISSUER="${MDB_RESOURCE_NAME}-selfsigned-issuer"
CA_CERT_NAME="${MDB_TLS_CA_SECRET_NAME}"   # Certificate resource name (same as CA secret)
CA_ISSUER="${MDB_RESOURCE_NAME}-ca-issuer"
SERVER_CERT_NAME="${MDB_RESOURCE_NAME}-server-tls"
SEARCH_CERT_NAME="${MDB_RESOURCE_NAME}-search-tls"

# Self-signed root issuer
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: ${SELF_ISSUER}
  namespace: ${MDB_NS}
spec:
  selfSigned: {}
EOF
kubectl wait --context "${K8S_CTX}" -n "${MDB_NS}" --for=condition=Ready issuer "${SELF_ISSUER}" --timeout=120s

# CA certificate (isCA=true) -> secret ${MDB_TLS_CA_SECRET_NAME}
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${CA_CERT_NAME}
  namespace: ${MDB_NS}
spec:
  isCA: true
  secretName: ${MDB_TLS_CA_SECRET_NAME}
  commonName: ${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local
  privateKey:
    algorithm: RSA
    size: 2048
  issuerRef:
    kind: Issuer
    name: ${SELF_ISSUER}
  duration: 168h
  renewBefore: 84h
EOF
kubectl wait --context "${K8S_CTX}" -n "${MDB_NS}" --for=condition=Ready certificate "${CA_CERT_NAME}" --timeout=300s

# CA issuer referencing CA secret
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: ${CA_ISSUER}
  namespace: ${MDB_NS}
spec:
  ca:
    secretName: ${MDB_TLS_CA_SECRET_NAME}
EOF
kubectl wait --context "${K8S_CTX}" -n "${MDB_NS}" --for=condition=Ready issuer "${CA_ISSUER}" --timeout=120s

members=${MDB_MEMBERS:-3}
SERVER_DNS=()
for ((i=0;i<members;i++)); do SERVER_DNS+=("${MDB_RESOURCE_NAME}-${i}"); done
for ((i=0;i<members;i++)); do SERVER_DNS+=("${MDB_RESOURCE_NAME}-${i}.${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local"); done
SERVER_DNS+=("${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local" "*.${MDB_RESOURCE_NAME}-svc.${MDB_NS}.svc.cluster.local")
SEARCH_DNS=("${MDB_RESOURCE_NAME}-search-0" "${MDB_RESOURCE_NAME}-search-0.${MDB_RESOURCE_NAME}-search-svc.${MDB_NS}.svc.cluster.local" "${MDB_RESOURCE_NAME}-search-svc.${MDB_NS}.svc.cluster.local")
join_dns() { local arr=("$@"); for d in "${arr[@]}"; do printf "      - \"%s\"\n" "$d"; done; }

# Server certificate
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${SERVER_CERT_NAME}
  namespace: ${MDB_NS}
spec:
  secretName: ${MDB_TLS_SERVER_CERT_SECRET_NAME}
  issuerRef:
    kind: Issuer
    name: ${CA_ISSUER}
  duration: 168h
  renewBefore: 84h
  usages:
    - digital signature
    - key encipherment
    - server auth
    - client auth
  dnsNames:
$(join_dns "${SERVER_DNS[@]}")
EOF

# Search certificate
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${SEARCH_CERT_NAME}
  namespace: ${MDB_NS}
spec:
  secretName: ${MDB_SEARCH_TLS_SECRET_NAME}
  issuerRef:
    kind: Issuer
    name: ${CA_ISSUER}
  duration: 168h
  renewBefore: 84h
  usages:
    - digital signature
    - key encipherment
    - server auth
    - client auth
  dnsNames:
$(join_dns "${SEARCH_DNS[@]}")
EOF

kubectl wait --context "${K8S_CTX}" -n "${MDB_NS}" --for=condition=Ready certificate "${SERVER_CERT_NAME}" --timeout=300s
kubectl wait --context "${K8S_CTX}" -n "${MDB_NS}" --for=condition=Ready certificate "${SEARCH_CERT_NAME}" --timeout=300s

# Verify secrets presence + essential keys
declare -a expect_secrets=("${MDB_TLS_SERVER_CERT_SECRET_NAME}" "${MDB_SEARCH_TLS_SECRET_NAME}" "${MDB_TLS_CA_SECRET_NAME}")
for sec in "${expect_secrets[@]}"; do
  kubectl get secret "$sec" --context "${K8S_CTX}" -n "${MDB_NS}" >/dev/null 2>&1 || { echo "ERROR: secret $sec missing" >&2; exit 1; }
  echo "✓ Secret $sec present"
  if [[ "$sec" != "${MDB_TLS_CA_SECRET_NAME}" ]]; then
    kubectl get secret "$sec" --context "${K8S_CTX}" -n "${MDB_NS}" -o jsonpath='{.data.tls\.crt}' | grep -q . || { echo "ERROR: tls.crt missing in $sec" >&2; exit 1; }
    kubectl get secret "$sec" --context "${K8S_CTX}" -n "${MDB_NS}" -o jsonpath='{.data.tls\.key}' | grep -q . || { echo "ERROR: tls.key missing in $sec" >&2; exit 1; }
  fi
done

# Ensure CA secret has ca.crt
if ! kubectl get secret "${MDB_TLS_CA_SECRET_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" -o jsonpath='{.data.ca\.crt}' 2>/dev/null | grep -q .; then
  b64crt=$(kubectl get secret "${MDB_TLS_CA_SECRET_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" -o jsonpath='{.data.tls\.crt}' || true)
  [[ -n "$b64crt" ]] && kubectl patch secret "${MDB_TLS_CA_SECRET_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" -p '{"data":{"ca.crt":"'"$b64crt"'"}}'
fi

# CA ConfigMap
if ! kubectl get configmap "${MDB_TLS_CA_CONFIGMAP_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" >/dev/null 2>&1; then
  ca_b64=$(kubectl get secret "${MDB_TLS_CA_SECRET_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" -o jsonpath='{.data.ca\.crt}' || true)
  if [[ -n "$ca_b64" ]]; then
    tmp_ca_file="$(mktemp)"
    printf '%s' "$ca_b64" | base64 --decode > "${tmp_ca_file}"
    kubectl create configmap "${MDB_TLS_CA_CONFIGMAP_NAME}" \
      --context "${K8S_CTX}" \
      --from-file=ca-pem="${tmp_ca_file}" \
      --from-file=ca.crt="${tmp_ca_file}" \
      --dry-run=client -o yaml \
      | kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f -
    rm -f "${tmp_ca_file}"
  fi
fi

echo "Community cert-manager TLS assets ready."
