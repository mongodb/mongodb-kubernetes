kubectl exec -i --context "${K8S_CTX}" -n "${MDB_NS}" mongodb-tools-pod -- \
  mongosh --quiet "${MDB_CONNECTION_STRING}" <<'MONGOSH'
use sample_mflix;
db.movies.getSearchIndexes();
db.embedded_movies.getSearchIndexes();
MONGOSH
