#!/usr/bin/env bash
# Generate TLS certificate for the MongoDB replica set
#
# DNS naming pattern: <pod>-<ordinal>.<headless-svc>.<namespace>.svc.cluster.local
# A single certificate covers all RS members using a wildcard SAN.

echo "Generating TLS certificate for MongoDB replica set..."

cert_name="${MDB_TLS_CERT_SECRET_PREFIX}-${MDB_EXTERNAL_CLUSTER_NAME}-cert"

dns_names="    - \"*.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local\""

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ${cert_name}
spec:
  secretName: ${cert_name}
  duration: 8760h    # 1 year
  renewBefore: 720h  # 30 days
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - server auth
    - client auth
  dnsNames:
${dns_names}
  issuerRef:
    name: ${MDB_TLS_CA_ISSUER}
    kind: ClusterIssuer
EOF
echo "  ✓ Certificate requested: ${cert_name}"

echo "Waiting for certificate to be ready..."
kubectl wait --for=condition=Ready certificate/"${cert_name}" \
  -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  --timeout=60s

echo "✓ MongoDB RS TLS certificate created"
