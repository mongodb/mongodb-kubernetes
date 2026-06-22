echo "Creating MongoDBSearch resource (multi-cluster sharded, managed Envoy LB)..."
echo "  Configuring ${MDB_MONGOT_REPLICAS_PER_CLUSTER} mongot replica(s) per (cluster, shard)"

# - source.external.shardedCluster lists the mongos routers and, per shard, the
#   per-cluster mongod pod FQDNs (reachable cross-cluster over the mesh). Pods
#   are named <resource>-<shardIndex>-<clusterIndex>-<memberIndex>-svc.
# - loadBalancer.managed makes the operator deploy a per-cluster Envoy that
#   fronts the mongot pods. externalHostname uses the {clusterIndex} and
#   {shardName} placeholders, which the operator substitutes per (cluster,shard)
#   to form each per-shard proxy Service FQDN. For a sharded source the value
#   must contain {shardName}; mongos search routing uses the cluster-level
#   proxy Service set on spec.mongos.additionalMongodConfig in 13_0310.
# - clusters[] places mongot replicas in each member cluster. Managed LB is
#   mandatory when clusters > 1.
kubectl apply --context "${K8S_CTX_0}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: ${MDB_SEARCH_RESOURCE_NAME}
spec:
  source:
    username: search-sync-source
    passwordSecretRef:
      name: ${MDB_RESOURCE_NAME}-search-sync-source-password
      key: password
    external:
      shardedCluster:
        router:
          hosts:
            - "${MDB_MONGOS_HOST_0}"
            - "${MDB_MONGOS_HOST_1}"
        shards:
          - shardName: ${MDB_SHARD_0_NAME}
            hosts:
              - "${MDB_RESOURCE_NAME}-0-0-0-svc.${MDB_NS}.svc.cluster.local:27017"
              - "${MDB_RESOURCE_NAME}-0-1-0-svc.${MDB_NS}.svc.cluster.local:27017"
          - shardName: ${MDB_SHARD_1_NAME}
            hosts:
              - "${MDB_RESOURCE_NAME}-1-0-0-svc.${MDB_NS}.svc.cluster.local:27017"
              - "${MDB_RESOURCE_NAME}-1-1-0-svc.${MDB_NS}.svc.cluster.local:27017"
          - shardName: ${MDB_SHARD_2_NAME}
            hosts:
              - "${MDB_RESOURCE_NAME}-2-0-0-svc.${MDB_NS}.svc.cluster.local:27017"
              - "${MDB_RESOURCE_NAME}-2-1-0-svc.${MDB_NS}.svc.cluster.local:27017"
      tls:
        ca:
          name: ${MDB_TLS_CA_SECRET_NAME}
  security:
    tls:
      certsSecretPrefix: ${MDB_TLS_CERT_SECRET_PREFIX}
  loadBalancer:
    managed:
      externalHostname: "${MDB_SEARCH_RESOURCE_NAME}-search-{clusterIndex}-{shardName}-proxy-svc.${MDB_NS}.svc.cluster.local"
  clusters:
    - clusterName: ${K8S_CTX_0}
      replicas: ${MDB_MONGOT_REPLICAS_PER_CLUSTER}
    - clusterName: ${K8S_CTX_1}
      replicas: ${MDB_MONGOT_REPLICAS_PER_CLUSTER}
EOF

echo "[ok] MongoDBSearch resource '${MDB_SEARCH_RESOURCE_NAME}' created"
