# List vector search indexes on the embedded_movies collection

echo "Listing vector search indexes on 'embedded_movies' collection..."

kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
  db.getSiblingDB("sample_mflix").embedded_movies.getSearchIndexes()
'
EOF
)"
