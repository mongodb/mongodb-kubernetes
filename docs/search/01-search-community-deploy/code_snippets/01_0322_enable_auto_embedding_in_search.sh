kubectl create secret generic "${AUTO_EMBEDDING_API_KEY_SECRET_NAME}" \
  --from-literal=query-key="${AUTO_EMBEDDING_API_QUERY_KEY}" \
  --from-literal=indexing-key="${AUTO_EMBEDDING_API_INDEXING_KEY}" --context "${K8S_CTX}" -n "${MDB_NS}"

kubectl patch mdbs "${MDB_RESOURCE_NAME}" --context "${K8S_CTX}" -n "${MDB_NS}" --type=merge -p '
{
  "spec": {
    "autoEmbedding": {
      "providerEndpoint": "'"${PROVIDER_ENDPOINT}"'",
      "embeddingModelAPIKeySecret": {
        "name": "'"${AUTO_EMBEDDING_API_KEY_SECRET_NAME}"'"
      }
    }
  }
}'
