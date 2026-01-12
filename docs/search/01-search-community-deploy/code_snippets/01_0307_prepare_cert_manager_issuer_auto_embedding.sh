# Bootstrap a self-signed ClusterIssuer that will mint the CA material consumed by
# the MongoDBCommunity deployment.
kubectl apply --context "${K8S_CTX}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${MDB_TLS_SELF_SIGNED_ISSUER_AUTO_EMBEDDING}
spec:
  selfSigned: {}
EOF_MANIFEST

kubectl --context "${K8S_CTX}" wait --for=condition=Ready clusterissuer "${MDB_TLS_SELF_SIGNED_ISSUER_AUTO_EMBEDDING}"

# Create the CA certificate and secret in the cert-manager namespace.
kubectl apply --context "${K8S_CTX}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${MDB_TLS_CA_CERT_NAME_AUTO_EMBEDDING}
  namespace: ${CERT_MANAGER_NAMESPACE}
spec:
  isCA: true
  commonName: ${MDB_TLS_CA_CERT_NAME_AUTO_EMBEDDING}
  secretName: ${MDB_TLS_CA_SECRET_NAME_AUTO_EMBEDDING}
  privateKey:
    algorithm: ECDSA
    size: 256
  issuerRef:
    name: ${MDB_TLS_SELF_SIGNED_ISSUER_AUTO_EMBEDDING}
    kind: ClusterIssuer
EOF_MANIFEST

kubectl --context "${K8S_CTX}" wait --for=condition=Ready -n "${CERT_MANAGER_NAMESPACE}" certificate "${MDB_TLS_CA_CERT_NAME_AUTO_EMBEDDING}"

# Publish a cluster-scoped issuer that fronts the generated CA secret so all namespaces can reuse it.
kubectl apply --context "${K8S_CTX}" -f - <<EOF_MANIFEST
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${MDB_TLS_CA_ISSUER_AUTO_EMBEDDING}
spec:
  ca:
    secretName: ${MDB_TLS_CA_SECRET_NAME_AUTO_EMBEDDING}
EOF_MANIFEST

kubectl --context "${K8S_CTX}" wait --for=condition=Ready clusterissuer "${MDB_TLS_CA_ISSUER_AUTO_EMBEDDING}"

TMP_CA_CERT="$(mktemp)"

kubectl --context "${K8S_CTX}" \
  get secret "${MDB_TLS_CA_SECRET_NAME_AUTO_EMBEDDING}" -n "${CERT_MANAGER_NAMESPACE}" \
  -o jsonpath="{.data['ca\\.crt']}" | base64 --decode > "${TMP_CA_CERT}"

# Expose the CA bundle through a ConfigMap for workloads and the MongoDBCommunity resource.
kubectl --context "${K8S_CTX}" create configmap "${MDB_TLS_CA_CONFIGMAP_AUTO_EMBEDDING}" -n "${MDB_NS}" \
  --from-file=ca-pem="${TMP_CA_CERT}" --from-file=mms-ca.crt="${TMP_CA_CERT}" \
  --from-file=ca.crt="${TMP_CA_CERT}" \
  --dry-run=client -o yaml | kubectl --context "${K8S_CTX}" apply -f -

echo "Cluster-wide CA issuer ${MDB_TLS_CA_ISSUER_AUTO_EMBEDDING} is ready."
