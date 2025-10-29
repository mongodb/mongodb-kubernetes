kubectl apply --context "${K8S_CTX}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${MDB_TLS_SELF_SIGNED_CLUSTER_ISSUER}
spec:
  selfSigned: {}
EOF_MANIFEST

kubectl --context "${K8S_CTX}" wait --for=condition=Ready clusterissuer "${MDB_TLS_SELF_SIGNED_CLUSTER_ISSUER}"

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
    name: ${MDB_TLS_SELF_SIGNED_CLUSTER_ISSUER}
    kind: ClusterIssuer
EOF_MANIFEST

kubectl --context "${K8S_CTX}" wait --for=condition=Ready -n "${CERT_MANAGER_NAMESPACE}" certificate "${MDB_TLS_CA_CERT_NAME}"

kubectl apply --context "${K8S_CTX}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${MDB_TLS_CLUSTER_ISSUER}
spec:
  ca:
    secretName: ${MDB_TLS_CA_SECRET_NAME}
EOF_MANIFEST

kubectl --context "${K8S_CTX}" wait --for=condition=Ready clusterissuer "${MDB_TLS_CLUSTER_ISSUER}"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

openssl s_client -showcerts -verify 2 \
  -connect downloads.mongodb.com:443 -servername downloads.mongodb.com < /dev/null \
  | awk -v DIR="${TMP_DIR}" '/BEGIN/,/END/{ if(/BEGIN/){a++}; out=DIR"/cert"a".crt"; print > out }'

kubectl --context "${K8S_CTX}" get secret "${MDB_TLS_CA_SECRET_NAME}" -n "${CERT_MANAGER_NAMESPACE}" -o jsonpath="{.data['ca\\.crt']}" | base64 --decode > "${TMP_DIR}/ca.crt"

# Build the full CA chain dynamically instead of assuming cert2/3 names
chain_files=$(ls "${TMP_DIR}"/cert*.crt 2>/dev/null | sort || true)
if [ -z "${chain_files}" ]; then
  echo "Warning: No intermediate certificates captured from downloads.mongodb.com; proceeding with ca.crt only" >&2
  cat "${TMP_DIR}/ca.crt" > "${TMP_DIR}/mms-ca.crt"
else
  cat "${TMP_DIR}/ca.crt" ${chain_files} > "${TMP_DIR}/mms-ca.crt"
fi

kubectl --context "${K8S_CTX}" create configmap "${MDB_TLS_CA_CONFIGMAP}" -n "${MDB_NS}" \
  --from-file=ca-pem="${TMP_DIR}/mms-ca.crt" --from-file=mms-ca.crt="${TMP_DIR}/mms-ca.crt" \
  --dry-run=client -o yaml | kubectl --context "${K8S_CTX}" apply -f -

# Ensure CA secret also exists in application namespace for mounts expecting a Secret (root-secret)
if ! kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get secret "${MDB_TLS_CA_SECRET_NAME}" >/dev/null 2>&1; then
  kubectl --context "${K8S_CTX}" -n "${CERT_MANAGER_NAMESPACE}" get secret "${MDB_TLS_CA_SECRET_NAME}" -o yaml \
    | sed 's/namespace: "+${CERT_MANAGER_NAMESPACE}+"/namespace: '"${MDB_NS}"'/' \
    | sed 's/namespace: cert-manager/namespace: '"${MDB_NS}"'/' \
    | kubectl --context "${K8S_CTX}" apply -n "${MDB_NS}" -f - || echo "Warning: failed to copy ${MDB_TLS_CA_SECRET_NAME} to ${MDB_NS}" >&2
fi
