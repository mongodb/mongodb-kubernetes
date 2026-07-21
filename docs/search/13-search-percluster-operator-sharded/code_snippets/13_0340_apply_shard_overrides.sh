echo "Optional: giving shard '${MDB_SHARD_0_NAME}' extra mongot capacity in cluster ${MDBS_CLUSTER_0_NAME} only..."

# JSON merge patch replaces list fields wholesale, so spec.clusters[] must be
# re-applied in full (kubectl apply), not patched -- otherwise a partial patch
# would drop cluster 1's entry instead of adding this override alongside it.
# shardOverrides lives inside spec.clusters[].shardOverrides, part of the SAME
# unified YAML applied everywhere: cluster 1's operator narrows to its own
# entry (no override there) and ignores it; only cluster 0's operator resolves
# this override for shard mdb-sh-0.
render_mongodb_search_with_override() {
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
      shardOverrides:
        - shardNames: ["${MDB_SHARD_0_NAME}"]
          replicas: 3
          resourceRequirements:
            requests:
              cpu: "2"
              memory: 4Gi
            limits:
              cpu: "4"
              memory: 8Gi
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
  render_mongodb_search_with_override | kubectl apply --context "${ctx}" -n "${MDB_NAMESPACE}" -f -
  echo "[ok] shardOverrides applied to cluster ${ctx}"
done

echo "[ok] ${MDBS_RESOURCE_NAME}-search-${MDBS_CLUSTER_0_INDEX}-${MDB_SHARD_0_NAME} will scale to 3 replicas; cluster 1's shard-0 mongot is unaffected"
