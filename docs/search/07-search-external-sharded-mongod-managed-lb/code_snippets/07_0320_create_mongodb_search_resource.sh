#!/usr/bin/env bash
# Create MongoDBSearch resource with External Sharded Source + Managed Envoy LB
# Traffic flow: External mongod → Envoy (port 27029) → mongot (port 27028)

echo "Creating MongoDBSearch resource with managed Envoy LB..."

# Build router hosts dynamically
router_hosts=""
for ((i = 0; i < MDB_MONGOS_COUNT; i++)); do
  host="${MDB_EXTERNAL_CLUSTER_NAME}-mongos-${i}.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local:27017"
  router_hosts="${router_hosts}
            - ${host}"
done

# Build shards configuration (outer loop: shards, inner loop: members)
shards_config=""
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"

  hosts=""
  for ((member = 0; member < MDB_MONGODS_PER_SHARD; member++)); do
    host="${shard_name}-${member}.${MDB_EXTERNAL_CLUSTER_NAME}-sh.${MDB_NS}.svc.cluster.local:27017"
    hosts="${hosts}
              - ${host}"
  done

  shards_config="${shards_config}
          - shardName: ${shard_name}
            hosts:${hosts}"
done

# Add replicas only if > 1 (operator defaults to 1)
replicas_spec=""
if [[ "${MDB_MONGOT_REPLICAS:-1}" -gt 1 ]]; then
  replicas_spec="
  replicas: ${MDB_MONGOT_REPLICAS}"
  echo "  Configuring ${MDB_MONGOT_REPLICAS} mongot replicas per shard"
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
      shardedCluster:
        router:
          hosts:${router_hosts}
        shards:${shards_config}
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
