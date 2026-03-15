#!/usr/bin/env bash
# Create MongoDBSearch resource with External Sharded Source + Managed Envoy LB
#
# This is the KEY CONFIGURATION for managed Envoy load balancing:
# - spec.source.external.shardedCluster: Points to the external MongoDB shards
# - spec.lb.mode: Managed - tells the operator to deploy Envoy automatically
#
# What the operator will create automatically:
# 1. Envoy ConfigMap with SNI-based routing configuration
# 2. Envoy Deployment with TLS certificates mounted
# 3. Per-shard proxy Services for traffic routing
#
# Traffic flow:
#   External mongod → Envoy (port 27029) → mongot (port 27028)
#
# NOTE: There is NO spec.lb.endpoint - the operator creates the endpoints!

echo "Creating MongoDBSearch resource with managed Envoy LB..."

# Build router hosts and shards configuration dynamically based on env vars.
# Example output for 1 mongos, 2 shards:
#   router.hosts: [ext-mdb-sh-mongos-0.ext-mdb-sh-svc.mongodb.svc.cluster.local:27017]
#   shards: [{shardName: ext-mdb-sh-0, hosts: [...]}, {shardName: ext-mdb-sh-1, hosts: [...]}]
router_hosts=""
for ((i = 0; i < MDB_MONGOS_COUNT; i++)); do
  host="${MDB_EXTERNAL_CLUSTER_NAME}-mongos-${i}.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local:27017"
  # Append with proper YAML indentation (12 spaces for nested list item)
  router_hosts="${router_hosts}
            - ${host}"
done

# Build shards configuration dynamically (outer loop: shards, inner loop: members)
shards_config=""
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"

  # Build hosts list for this shard (each member in the replica set)
  hosts=""
  for ((member = 0; member < MDB_MONGODS_PER_SHARD; member++)); do
    host="${shard_name}-${member}.${MDB_EXTERNAL_CLUSTER_NAME}-sh.${MDB_NS}.svc.cluster.local:27017"
    # 14 spaces for deeply nested list item
    hosts="${hosts}
              - ${host}"
  done

  # 10 spaces for shard entry in shards list
  shards_config="${shards_config}
          - shardName: ${shard_name}
            hosts:${hosts}"
done

# Add replicas only if > 1 (operator defaults to 1)
# [[ "$a" -gt "$b" ]] means "a greater than b" in bash
replicas_spec=""
if [[ "${MDB_MONGOT_REPLICAS:-1}" -gt 1 ]]; then
  replicas_spec="
  replicas: ${MDB_MONGOT_REPLICAS}"
  echo "  Configuring ${MDB_MONGOT_REPLICAS} mongot replicas per shard"
fi

# ============================================================================
# GENERATED YAML PREVIEW
# ============================================================================
# This shows the key dynamic sections before applying.
# Useful for debugging and understanding what will be created.
# ============================================================================
echo ""
echo "=== Generated Configuration Preview ==="
echo ""
echo "Router hosts:${router_hosts}"
echo ""
echo "Shards:${shards_config}"
echo ""
if [[ -n "${replicas_spec}" ]]; then
  echo "Replicas:${replicas_spec}"
  echo ""
fi
echo "=== End Preview ==="
echo ""
echo "Applying MongoDBSearch resource..."

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: ${MDB_SEARCH_RESOURCE_NAME}
spec:
  logLevel: DEBUG${replicas_spec}
  source:
    # Credentials for mongot to connect to MongoDB
    username: search-sync-source
    passwordSecretRef:
      name: ${MDB_SEARCH_RESOURCE_NAME}-search-sync-source-password
      key: password
    # External sharded cluster source configuration
    external:
      shardedCluster:
        router:
          hosts:${router_hosts}
        shards:${shards_config}
      tls:
        ca:
          name: ${MDB_TLS_CA_SECRET_NAME}
  security:
    tls:
      # Per-shard TLS: each shard uses {prefix}-{shardName}-search-cert
      certsSecretPrefix: ${MDB_SEARCH_TLS_CERT_PREFIX}
  # MANAGED ENVOY CONFIGURATION
  # The operator automatically deploys and configures Envoy proxy
  lb:
    mode: Managed
  # NOTE: No spec.lb.endpoint - the operator creates proxy Services automatically!
  resourceRequirements:
    limits:
      cpu: "2"
      memory: 3Gi
    requests:
      cpu: "1"
      memory: 2Gi
EOF

echo "✓ MongoDBSearch resource '${MDB_SEARCH_RESOURCE_NAME}' created"
echo ""
echo "The operator will now:"
echo "  1. Create mongot StatefulSets for each shard"
echo "  2. Deploy Envoy proxy (managed LB)"
echo "  3. Create per-shard proxy Services for SNI routing"
echo ""
echo "Proxy Service endpoints (created by operator):"
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  echo "  - ${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-proxy-svc:${ENVOY_PROXY_PORT:-27029}"
done
