# Bootstrap a self-signed Issuer scoped to the cert-manager namespace. This is
# only used to mint the CA secret and is not referenced by application
# workloads.
kubectl apply --context "${K8S_CTX}" -n "${CERT_MANAGER_NAMESPACE}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: ${MDB_TLS_SELF_SIGNED_ISSUER}
spec:
  selfSigned: {}
EOF_MANIFEST

kubectl --context "${K8S_CTX}" wait --namespace "${CERT_MANAGER_NAMESPACE}" --for=condition=Ready issuer "${MDB_TLS_SELF_SIGNED_ISSUER}"

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
    kind: Issuer
EOF_MANIFEST

kubectl --context "${K8S_CTX}" wait --for=condition=Ready -n "${CERT_MANAGER_NAMESPACE}" certificate "${MDB_TLS_CA_CERT_NAME}"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

kubectl --context "${K8S_CTX}" get secret "${MDB_TLS_CA_SECRET_NAME}" -n "${CERT_MANAGER_NAMESPACE}" -o jsonpath="{.data['ca\\.crt']}" | base64 --decode > "${TMP_DIR}/ca.crt"

cat "${TMP_DIR}/ca.crt" > "${TMP_DIR}/mms-ca.crt"

kubectl --context "${K8S_CTX}" create configmap "${MDB_TLS_CA_CONFIGMAP}" -n "${MDB_NS}" \
  --from-file=ca-pem="${TMP_DIR}/mms-ca.crt" --from-file=mms-ca.crt="${TMP_DIR}/mms-ca.crt" \
  --dry-run=client -o yaml | kubectl --context "${K8S_CTX}" apply -f -

# Ensure CA secret also exists in application namespace for mounts expecting a Secret (root-secret)
if ! kubectl --context "${K8S_CTX}" -n "${MDB_NS}" get secret "${MDB_TLS_CA_SECRET_NAME}" >/dev/null 2>&1; then
  kubectl --context "${K8S_CTX}" -n "${CERT_MANAGER_NAMESPACE}" get secret "${MDB_TLS_CA_SECRET_NAME}" -o yaml \
    | sed 's/namespace: .*/namespace: '"${MDB_NS}"'/' \
    | kubectl --context "${K8S_CTX}" apply -n "${MDB_NS}" -f - || echo "Warning: failed to copy ${MDB_TLS_CA_SECRET_NAME} to ${MDB_NS}" >&2
fi

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: ${MDB_TLS_CA_ISSUER}
spec:
  ca:
    secretName: ${MDB_TLS_CA_SECRET_NAME}
EOF_MANIFEST

kubectl --context "${K8S_CTX}" wait --namespace "${MDB_NS}" --for=condition=Ready issuer "${MDB_TLS_CA_ISSUER}"
