#!/usr/bin/env bash
# Create MongoDBSearch resource with Operator-Managed RS Source + Managed Envoy LB
# Traffic flow: Operator-managed mongod → Envoy (port 27029) → mongot (port 27028)
#
# Layout: 3 RS members, 2 mongot replicas (single StatefulSet)
#
# Topology: single mongot StatefulSet and single LB proxy Service

echo "Creating MongoDBSearch resource with managed Envoy LB..."
echo "  Configuring ${MDB_MONGOT_REPLICAS} mongot replicas"

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: ${MDB_RESOURCE_NAME}
spec:
  logLevel: DEBUG
  replicas: ${MDB_MONGOT_REPLICAS}
  source:
    mongodbResourceRef:
      name: ${MDB_RESOURCE_NAME}
  security:
    tls:
      certsSecretPrefix: ${MDB_TLS_CERT_SECRET_PREFIX}
  # lb.mode: Managed — operator automatically deploys and configures Envoy proxy
  lb:
    mode: Managed
  resourceRequirements:
    limits:
      cpu: "2"
      memory: 3Gi
    requests:
      cpu: "1"
      memory: 2Gi
EOF

echo "✓ MongoDBSearch resource '${MDB_RESOURCE_NAME}' created"
