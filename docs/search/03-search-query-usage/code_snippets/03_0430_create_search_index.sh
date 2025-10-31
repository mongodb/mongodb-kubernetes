kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" mongodb-tools-pod -- \
  mongosh --quiet --tls --tlsCAFile /tls/ca.crt "${MDB_CONNECTION_STRING}" \
    --eval "use sample_mflix" \
    --eval 'db.movies.createSearchIndex("default", { mappings: { dynamic: true } });'
