echo "Applying the unified MongoDBSearch CR to every member cluster..."
echo "This is the SAME manifest, applied independently to each cluster's own operator."

search_cr=$(cat <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: ${SEARCH_RESOURCE_NAME}
spec:
  source:
    username: ${SEARCH_SYNC_USER_NAME}
    passwordSecretRef:
      name: ${SEARCH_RESOURCE_NAME}-${SEARCH_SYNC_USER_NAME}-password
      key: password
    external:
      hostAndPorts:
        - ${SEARCH_SOURCE_SEED_0_0}
        - ${SEARCH_SOURCE_SEED_0_1}
        - ${SEARCH_SOURCE_SEED_1_0}
        - ${SEARCH_SOURCE_SEED_2_0}
        - ${SEARCH_SOURCE_SEED_2_1}
      tls:
        ca:
          name: ${SOURCE_CA_CONFIGMAP}
  security:
    tls:
      certsSecretPrefix: ${SEARCH_TLS_CERT_SECRET_PREFIX}
  clusters:
    - name: ${SEARCH_CLUSTER_0_NAME}
      index: ${SEARCH_CLUSTER_0_INDEX}
      replicas: ${SEARCH_MONGOT_REPLICAS}
      loadBalancer:
        managed:
          replicas: ${SEARCH_ENVOY_LB_REPLICAS}
          externalHostname: ${SEARCH_PROXY_SVC_0}
    - name: ${SEARCH_CLUSTER_1_NAME}
      index: ${SEARCH_CLUSTER_1_INDEX}
      replicas: ${SEARCH_MONGOT_REPLICAS}
      loadBalancer:
        managed:
          replicas: ${SEARCH_ENVOY_LB_REPLICAS}
          externalHostname: ${SEARCH_PROXY_SVC_1}
    - name: ${SEARCH_CLUSTER_2_NAME}
      index: ${SEARCH_CLUSTER_2_INDEX}
      replicas: ${SEARCH_MONGOT_REPLICAS}
      loadBalancer:
        managed:
          replicas: ${SEARCH_ENVOY_LB_REPLICAS}
          externalHostname: ${SEARCH_PROXY_SVC_2}
EOF
)

for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  echo "${search_cr}" | kubectl apply --context "${ctx}" -n "${MDB_NAMESPACE}" -f -
  echo "  [ok] MongoDBSearch '${SEARCH_RESOURCE_NAME}' applied to ${ctx}"
done
