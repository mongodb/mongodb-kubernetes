echo "Applying MongoDBSearch CR (Q2-MC RS, mirrors spec §4.1)..."

kubectl apply --context "${K8S_CENTRAL_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: ${MDB_SEARCH_RESOURCE_NAME}
  namespace: ${MDB_NS}
spec:
  source:
    external:
      hostAndPorts:
        - ${MDB_EXTERNAL_HOST_0}
        - ${MDB_EXTERNAL_HOST_1}
        - ${MDB_EXTERNAL_HOST_2}
      tls:
        ca:
          name: ${MDB_EXTERNAL_CA_SECRET}
    username: ${MDB_SEARCH_SYNC_USERNAME}
    passwordSecretRef:
      name: ${MDB_SYNC_PASSWORD_SECRET}
      key: password
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
