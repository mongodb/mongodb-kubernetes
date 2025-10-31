required=(
  K8S_CTX
  MDB_NS
  MDB_RESOURCE_NAME
  MDB_MEMBERS
  MDB_TLS_CA_SECRET_NAME
  MDB_TLS_SERVER_CERT_SECRET_NAME
  MDB_SEARCH_TLS_SECRET_NAME
  CERT_MANAGER_NAMESPACE
)
missing=()

for var in "${required[@]}"; do
  [[ -n "${!var:-}" ]] || missing+=("${var}")
done
if (( ${#missing[@]} )); then
  echo "Missing required environment variables: ${missing[*]}" >&2
  exit 1
fi

helm repo add jetstack https://charts.jetstack.io --force-update >/dev/null 2>&1 || true
helm upgrade --install \
  cert-manager jetstack/cert-manager \
  --kube-context "${K8S_CTX}" \
  --namespace "${CERT_MANAGER_NAMESPACE}" \
  --create-namespace \
  --set crds.enabled=true >/dev/null 2>&1

for deployment in cert-manager cert-manager-cainjector cert-manager-webhook; do
  kubectl --context "${K8S_CTX}" \
    -n "${CERT_MANAGER_NAMESPACE}" \
    wait --for=condition=Available "deployment/${deployment}" --timeout=300s
done

self_signed_issuer="${MDB_RESOURCE_NAME}-selfsigned-issuer"
ca_cert_name="${MDB_RESOURCE_NAME}-ca"
ca_issuer="${MDB_RESOURCE_NAME}-ca-issuer"
server_certificate="${MDB_RESOURCE_NAME}-server-tls"
search_certificate="${MDB_RESOURCE_NAME}-search-tls"

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: ${self_signed_issuer}
  namespace: ${MDB_NS}
spec:
  selfSigned: {}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${ca_cert_name}
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
    name: ${self_signed_issuer}
  duration: 240h0m0s
  renewBefore: 120h0m0s
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: ${ca_issuer}
  namespace: ${MDB_NS}
spec:
  ca:
    secretName: ${MDB_TLS_CA_SECRET_NAME}
EOF_MANIFEST

kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready issuer "${self_signed_issuer}" --timeout=120s
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready certificate "${ca_cert_name}" --timeout=300s
kubectl --context "${K8S_CTX}" -n "${MDB_NS}" wait --for=condition=Ready issuer "${ca_issuer}" --timeout=120s

if ! kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get secret "${MDB_TLS_CA_SECRET_NAME}" -o jsonpath='{.data.ca\\.crt}' 2>/dev/null | grep -q .; then
  tls_crt=$(kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get secret "${MDB_TLS_CA_SECRET_NAME}" -o jsonpath='{.data.tls\\.crt}' || true)
  if [[ -n "${tls_crt}" ]]; then
    kubectl --context "${K8S_CTX}" -n "${MDB_NS}" patch secret "${MDB_TLS_CA_SECRET_NAME}" \
      --type=merge \
      -p "{"data":{"ca.crt":"${tls_crt}"}}"
  fi
fi

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

echo "Community cert-manager TLS assets ready."
