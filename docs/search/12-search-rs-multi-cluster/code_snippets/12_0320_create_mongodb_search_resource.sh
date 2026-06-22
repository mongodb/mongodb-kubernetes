echo "Creating MongoDBSearch resource (multi-cluster, managed Envoy LB)..."
echo "  Configuring ${MDB_MONGOT_REPLICAS_PER_CLUSTER} mongot replica(s) per member cluster"

# - source.external.hostAndPorts is the seed list of every source mongod
#   member's per-pod Service FQDN (reachable cross-cluster over the mesh). The
#   host list matches MDB_MEMBERS_PER_CLUSTER=2 across the two member clusters.
# - loadBalancer.managed makes the operator deploy a per-cluster Envoy that
#   fronts the mongot pods. externalHostname uses the {clusterIndex}
#   placeholder, which the operator substitutes per cluster. Managed LB is
#   mandatory when clusters > 1.
# - clusters[] places mongot replicas in each member cluster.
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
      hostAndPorts:
        - "${MDB_RS_HOST_0_0}"
        - "${MDB_RS_HOST_0_1}"
        - "${MDB_RS_HOST_1_0}"
        - "${MDB_RS_HOST_1_1}"
      tls:
        ca:
          name: ${MDB_TLS_CA_SECRET_NAME}
  security:
    tls:
      certsSecretPrefix: ${MDB_TLS_CERT_SECRET_PREFIX}
  loadBalancer:
    managed:
      externalHostname: "${MDB_SEARCH_RESOURCE_NAME}-search-{clusterIndex}-proxy-svc.${MDB_NS}.svc.cluster.local"
  clusters:
    - clusterName: ${K8S_CTX_0}
      replicas: ${MDB_MONGOT_REPLICAS_PER_CLUSTER}
    - clusterName: ${K8S_CTX_1}
      replicas: ${MDB_MONGOT_REPLICAS_PER_CLUSTER}
EOF

echo "[ok] MongoDBSearch resource '${MDB_SEARCH_RESOURCE_NAME}' created"
