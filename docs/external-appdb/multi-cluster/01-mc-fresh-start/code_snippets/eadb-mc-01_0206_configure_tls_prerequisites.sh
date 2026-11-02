# Create the signing chain for the AppDB certificate: a self-signed ClusterIssuer bootstraps a CA
# Certificate, which backs a CA ClusterIssuer. We then publish the CA into a ConfigMap the operator
# references as spec.security.tls.ca (keys "ca-pem" and "mms-ca.crt").
echo "Configuring TLS prerequisites..."

kubectl apply --context "${K8S_CTX_0}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${APPDB_TLS_SELF_SIGNED_ISSUER}
spec:
  selfSigned: {}
EOF
echo "  [ok] Self-signed ClusterIssuer created"

kubectl apply --context "${K8S_CTX_0}" -n "${CERT_MANAGER_NAMESPACE}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${APPDB_TLS_CA_CERT_NAME}
spec:
  isCA: true
  commonName: mongodb-ca
  secretName: ${APPDB_TLS_CA_SECRET_NAME}
  duration: 87600h
  renewBefore: 8760h
  privateKey:
    algorithm: ECDSA
    size: 256
  issuerRef:
    name: ${APPDB_TLS_SELF_SIGNED_ISSUER}
    kind: ClusterIssuer
EOF
echo "  [ok] CA Certificate requested"

echo "  Waiting for CA certificate..."
kubectl wait --for=condition=Ready certificate/"${APPDB_TLS_CA_CERT_NAME}" \
  -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX_0}" --timeout=120s

kubectl apply --context "${K8S_CTX_0}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${APPDB_TLS_CA_ISSUER}
spec:
  ca:
    secretName: ${APPDB_TLS_CA_SECRET_NAME}
EOF
echo "  [ok] CA Issuer created"

# Publish the CA into a ConfigMap in the MongoDB namespace. The operator expects the CA that validates
# Ops Manager under the key "mms-ca.crt"; AppDB TLS reads "ca-pem". Populate both from the CA secret.
ca_crt=$(kubectl get secret "${APPDB_TLS_CA_SECRET_NAME}" \
  -n "${CERT_MANAGER_NAMESPACE}" --context "${K8S_CTX_0}" \
  -o jsonpath='{.data.ca\.crt}' | base64 -d)

# The AppDB automation agent downloads the MongoDB binary over HTTPS from downloads.mongodb.com, and
# when AppDB TLS is enabled it validates that download against this CA bundle (not the system store).
# So the bundle must trust BOTH the self-signed CA (for AppDB member TLS) AND the public roots that
# sign downloads.mongodb.com. Append the host's system CA bundle to cover the public roots.
system_ca=""
for candidate in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/cert.pem; do
  if [[ -r "${candidate}" ]]; then
    system_ca=$(cat "${candidate}")
    break
  fi
done
if [[ -z "${system_ca}" ]]; then
  echo "  [warn] no system CA bundle found; AppDB agent may fail to verify downloads.mongodb.com"
fi
# Write the combined bundle to a temp file and load it via --from-file: the system CA bundle is too
# large to pass through --from-literal (kubectl would hit "Argument list too long"). Apply it
# server-side: the bundle also exceeds the 262144-byte limit of the client-side apply
# last-applied-configuration annotation, which server-side apply does not use.
ca_bundle_file=$(mktemp)
printf '%s\n%s\n' "${ca_crt}" "${system_ca}" > "${ca_bundle_file}"

kubectl create configmap "${APPDB_CA_CONFIGMAP}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  --from-file=ca-pem="${ca_bundle_file}" \
  --from-file=mms-ca.crt="${ca_bundle_file}" \
  --dry-run=client -o yaml \
  | kubectl apply --server-side --force-conflicts --context "${K8S_CTX_0}" -n "${MDB_NS}" -f -

rm -f "${ca_bundle_file}"

echo "[ok] TLS prerequisites configured (CA ConfigMap '${APPDB_CA_CONFIGMAP}' ready)"
