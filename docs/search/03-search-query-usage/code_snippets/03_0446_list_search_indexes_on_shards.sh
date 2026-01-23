# List search indexes through mongos

echo "Listing search indexes..."

kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
  db.getSiblingDB("sample_mflix").movies.getSearchIndexes().forEach(idx => printjson(idx));
'
EOF
)"
