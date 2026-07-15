echo "Creating the unified MongoDBSearch resource in every member cluster..."
echo "  Configuring ${MDBS_MONGOT_REPLICAS} mongot replicas per (cluster, shard)"

# The SAME rendered YAML is applied to every cluster: each cluster's operator
# narrows spec.clusters[] down to its own entry (LocalizeToCluster) and only
# ever creates local, index-suffixed resources. Since there is only ONE
# source (not one per cluster), both clusters declare the SAME two shards --
# each cluster ends up with a full, independent set of mongot groups for
# every shard, not a partitioned subset.
render_mongodb_search() {
  cat <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: ${MDBS_RESOURCE_NAME}
spec:
  logLevel: DEBUG
  source:
    username: search-sync-source
    passwordSecretRef:
      name: ${MDBS_RESOURCE_NAME}-search-sync-source-password
      key: password
    external:
      shardedCluster:
        router:
          hosts:
            - ${MDB_MONGOS_HOST_0}
        shards:
          - shardName: ${MDB_SHARD_0_NAME}
            hosts:
              - ${MDB_SHARD_0_HOST_0}
          - shardName: ${MDB_SHARD_1_NAME}
            hosts:
              - ${MDB_SHARD_1_HOST_0}
      tls:
        ca:
          name: ${MDBS_TLS_CA_CONFIGMAP}
  security:
    tls:
      certsSecretPrefix: ${MDBS_TLS_CERT_SECRET_PREFIX}
  clusters:
    - name: ${MDBS_CLUSTER_0_NAME}
      index: ${MDBS_CLUSTER_0_INDEX}
      replicas: ${MDBS_MONGOT_REPLICAS}
      loadBalancer:
        managed:
          replicas: ${MDBS_ENVOY_LB_REPLICAS}
          externalHostname: ${MDBS_CLUSTER_0_EXTERNAL_HOSTNAME_TEMPLATE}
          routerHostname: ${MDBS_CLUSTER_0_ROUTER_HOSTNAME}
    - name: ${MDBS_CLUSTER_1_NAME}
      index: ${MDBS_CLUSTER_1_INDEX}
      replicas: ${MDBS_MONGOT_REPLICAS}
      loadBalancer:
        managed:
          replicas: ${MDBS_ENVOY_LB_REPLICAS}
          externalHostname: ${MDBS_CLUSTER_1_EXTERNAL_HOSTNAME_TEMPLATE}
          routerHostname: ${MDBS_CLUSTER_1_ROUTER_HOSTNAME}
EOF
}

for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}"; do
  render_mongodb_search | kubectl apply --context "${ctx}" -n "${MDB_NAMESPACE}" -f -
  echo "[ok] MongoDBSearch '${MDBS_RESOURCE_NAME}' applied to cluster ${ctx}"
done
