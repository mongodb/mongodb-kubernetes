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

kubectl create configmap "${APPDB_CA_CONFIGMAP}" \
  --context "${K8S_CTX_0}" -n "${MDB_NS}" \
  --from-literal=ca-pem="${ca_crt}" \
  --from-literal=mms-ca.crt="${ca_crt}" \
  --dry-run=client -o yaml \
  | kubectl apply --context "${K8S_CTX_0}" -n "${MDB_NS}" -f -

echo "[ok] TLS prerequisites configured (CA ConfigMap '${APPDB_CA_CONFIGMAP}' ready)"
