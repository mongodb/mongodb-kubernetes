echo "Creating MongoDBSearch resource (multi-cluster sharded, managed Envoy LB)..."
echo "  Configuring ${MDB_MONGOT_REPLICAS_PER_CLUSTER} mongot replica(s) per (cluster, shard)"

# - source.external.shardedCluster lists the mongos routers and, per shard, the
#   per-cluster mongod pod FQDNs (reachable cross-cluster over the mesh). Pods
#   are named <resource>-<shardIndex>-<clusterIndex>-<memberIndex>-svc.
# - clusters[].loadBalancer.managed makes the operator deploy a per-cluster Envoy
#   that fronts the mongot pods. Each cluster sets its own externalHostname with
#   the cluster index resolved literally (search-0-... / search-1-...) and the
#   {shardName} placeholder kept for the operator to substitute per shard, forming
#   each per-shard proxy Service FQDN. routerHostname is the shard-agnostic
#   cluster-level proxy Service FQDN that mongos uses for search routing (matches
#   spec.mongos.additionalMongodConfig set in 13_0310); it must NOT contain
#   {shardName}.
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
              - "${MDB_SHARD_0_HOST_CL0}"
              - "${MDB_SHARD_0_HOST_CL1}"
          - shardName: ${MDB_SHARD_1_NAME}
            hosts:
              - "${MDB_SHARD_1_HOST_CL0}"
              - "${MDB_SHARD_1_HOST_CL1}"
          - shardName: ${MDB_SHARD_2_NAME}
            hosts:
              - "${MDB_SHARD_2_HOST_CL0}"
              - "${MDB_SHARD_2_HOST_CL1}"
      tls:
        ca:
          name: ${MDB_TLS_CA_SECRET_NAME}
  security:
    tls:
      certsSecretPrefix: ${MDB_TLS_CERT_SECRET_PREFIX}
  clusters:
    - name: ${K8S_CTX_0}
      index: 0
      replicas: ${MDB_MONGOT_REPLICAS_PER_CLUSTER}
      loadBalancer:
        managed:
          externalHostname: "${MDB_SEARCH_RESOURCE_NAME}-search-0-{shardName}-proxy-svc.${MDB_NS}.svc.cluster.local"
          routerHostname: "${MDB_SEARCH_RESOURCE_NAME}-search-0-proxy-svc.${MDB_NS}.svc.cluster.local"
      resourceRequirements:
        limits:
          cpu: "1"
          memory: 2Gi
        requests:
          cpu: 500m
          memory: 2Gi
    - name: ${K8S_CTX_1}
      index: 1
      replicas: ${MDB_MONGOT_REPLICAS_PER_CLUSTER}
      loadBalancer:
        managed:
          externalHostname: "${MDB_SEARCH_RESOURCE_NAME}-search-1-{shardName}-proxy-svc.${MDB_NS}.svc.cluster.local"
          routerHostname: "${MDB_SEARCH_RESOURCE_NAME}-search-1-proxy-svc.${MDB_NS}.svc.cluster.local"
      resourceRequirements:
        limits:
          cpu: "1"
          memory: 2Gi
        requests:
          cpu: 500m
          memory: 2Gi
EOF

echo "[ok] MongoDBSearch resource '${MDB_SEARCH_RESOURCE_NAME}' created"
