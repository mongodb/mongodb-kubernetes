kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" mongodb-tools-pod-auto-embedding -- \
  mongosh --quiet "${MDB_CONNECTION_STRING_AUTO_EMBEDDING}" \
    --eval "use sample_mflix" \
    --eval 'db.movies.createSearchIndex("vector_index", "vectorSearch",
      { "fields": [ {
        "type": "autoEmbed",
        "path": "plot",
        "modality": "text",
        "model": "voyage-3.5-lite"
      } ] });'
