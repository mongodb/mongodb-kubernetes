echo "Applying MongoDBSearch CR (Q2-MC Sharded, mirrors spec §4.2)..."

kubectl apply --context "${K8S_CENTRAL_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: ${MDB_SEARCH_RESOURCE_NAME}
  namespace: ${MDB_NS}
spec:
  source:
    external:
      shardedCluster:
        router:
          hosts:
            - ${MDB_EXTERNAL_MONGOS_HOST_0}
            - ${MDB_EXTERNAL_MONGOS_HOST_1}
        shards:
          - shardName: ${MDB_SHARD_0_NAME}
            hosts:
              - ${MDB_SHARD_0_HOST_0}
              - ${MDB_SHARD_0_HOST_1}
              - ${MDB_SHARD_0_HOST_2}
          - shardName: ${MDB_SHARD_1_NAME}
            hosts:
              - ${MDB_SHARD_1_HOST_0}
              - ${MDB_SHARD_1_HOST_1}
              - ${MDB_SHARD_1_HOST_2}
      tls:
        ca:
          name: ${MDB_EXTERNAL_CA_SECRET}
      keyfileSecretRef:
        name: ${MDB_KEYFILE_SECRET}
    username: ${MDB_SEARCH_SYNC_USERNAME}
    passwordSecretRef:
      name: ${MDB_SYNC_PASSWORD_SECRET}
  loadBalancer:
    managed:
      externalHostname: "${MDB_LB_EXTERNAL_HOSTNAME_TEMPLATE}"
  security:
    tls:
      certsSecretPrefix: ${MDB_TLS_CERT_SECRET_PREFIX}
  clusters:
    - clusterName: ${MEMBER_CLUSTER_0_NAME}
      replicas: ${MDB_MONGOT_REPLICAS}
      syncSourceSelector:
        matchTags:
          region: ${MEMBER_CLUSTER_0_REGION}
    - clusterName: ${MEMBER_CLUSTER_1_NAME}
      replicas: ${MDB_MONGOT_REPLICAS}
      syncSourceSelector:
        matchTags:
          region: ${MEMBER_CLUSTER_1_REGION}
EOF

echo "[ok] MongoDBSearch '${MDB_SEARCH_RESOURCE_NAME}' applied"
