echo "Creating MongoDBSearch resource (multi-cluster, managed Envoy LB)..."
echo "  Configuring ${MDB_MONGOT_REPLICAS_PER_CLUSTER} mongot replica(s) per member cluster"

# - source.external.hostAndPorts is the seed list of every source mongod
#   member's per-pod Service FQDN (reachable cross-cluster over the mesh). The
#   host list matches MDB_MEMBERS_PER_CLUSTER=2 across the two member clusters.
# - clusters[].loadBalancer.managed makes the operator deploy a per-cluster Envoy
#   that fronts the mongot pods. Each cluster sets its own externalHostname (the
#   per-cluster proxy Service FQDN, with the cluster index resolved literally:
#   search-0-... for the first cluster, search-1-... for the second). Managed LB
#   is mandatory when clusters > 1.
# - clusters[] places mongot replicas in each member cluster.
# - resourceRequirements pins each cluster's mongot CPU/memory so the pods fit
#   the kind test nodes (the operator's default 2 CPU / 4Gi would exhaust a node).
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
          # The operator mounts this CA as a ConfigMap (key ca.crt) in the mongot
          # pod's namespace on every member cluster. Point at the CA ConfigMap
          # (created on both members by 12_0302a), not the cert-manager CA Secret.
          name: ${MDB_TLS_CA_CONFIGMAP}
  security:
    tls:
      certsSecretPrefix: ${MDB_TLS_CERT_SECRET_PREFIX}
  clusters:
    - name: ${K8S_CTX_0}
      index: 0
      replicas: ${MDB_MONGOT_REPLICAS_PER_CLUSTER}
      loadBalancer:
        managed:
          externalHostname: "${MDB_SEARCH_RESOURCE_NAME}-search-0-proxy-svc.${MDB_NS}.svc.cluster.local"
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
          externalHostname: "${MDB_SEARCH_RESOURCE_NAME}-search-1-proxy-svc.${MDB_NS}.svc.cluster.local"
      resourceRequirements:
        limits:
          cpu: "1"
          memory: 2Gi
        requests:
          cpu: 500m
          memory: 2Gi
EOF

echo "[ok] MongoDBSearch resource '${MDB_SEARCH_RESOURCE_NAME}' created"
