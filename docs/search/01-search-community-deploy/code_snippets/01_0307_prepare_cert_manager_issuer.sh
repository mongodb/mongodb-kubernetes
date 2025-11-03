self_signed_issuer="${MDB_RESOURCE_NAME}-selfsigned-issuer"
ca_cert_name="${MDB_RESOURCE_NAME}-ca"
ca_issuer="${MDB_RESOURCE_NAME}-ca-issuer"

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

echo "cert-manager issuer ${ca_issuer} is ready to sign certificates."
