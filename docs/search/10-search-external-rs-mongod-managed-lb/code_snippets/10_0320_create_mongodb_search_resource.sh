#!/usr/bin/env bash
# Create MongoDBSearch resource with External RS Source + Managed Envoy LB
# Traffic flow: External mongod → Envoy (port 27029) → mongot (port 27028)

echo "Creating MongoDBSearch resource with managed Envoy LB..."

# Build hostAndPorts list from external RS members
host_and_ports=""
for ((i = 0; i < MDB_RS_MEMBERS; i++)); do
  host_var="MDB_EXTERNAL_HOST_${i}"
  host_and_ports="${host_and_ports}
          - ${!host_var}"
done

# Add replicas only if > 1 (operator defaults to 1)
replicas_spec=""
if [[ "${MDB_MONGOT_REPLICAS:-1}" -gt 1 ]]; then
  replicas_spec="
  replicas: ${MDB_MONGOT_REPLICAS}"
  echo "  Configuring ${MDB_MONGOT_REPLICAS} mongot replicas"
fi

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: ${MDB_SEARCH_RESOURCE_NAME}
spec:
  logLevel: DEBUG${replicas_spec}
  source:
    username: search-sync-source
    passwordSecretRef:
      name: ${MDB_SEARCH_RESOURCE_NAME}-search-sync-source-password
      key: password
    external:
      hostAndPorts:${host_and_ports}
      tls:
        ca:
          name: ${MDB_TLS_CA_SECRET_NAME}
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

echo "✓ MongoDBSearch resource '${MDB_SEARCH_RESOURCE_NAME}' created"
