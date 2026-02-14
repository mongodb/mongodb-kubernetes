# Wait for search index to be ready (checking through mongos)

echo "Waiting for search index to be ready..."

max_attempts=60
sleep_time=5

for attempt in $(seq 1 ${max_attempts}); do
  status=$(kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
    mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
  const result = db.getSiblingDB("sample_mflix").runCommand({ listSearchIndexes: "movies" });
  if (result.ok && result.cursor && result.cursor.firstBatch && result.cursor.firstBatch.length > 0) {
    const idx = result.cursor.firstBatch[0];
    print(idx.status || "READY");
  } else {
    print("NO_INDEX");
  }
'
EOF
)" 2>/dev/null | tail -1)

  echo "Attempt ${attempt}/${max_attempts}: Index status = ${status}"

  if [[ "${status}" == "READY" ]]; then
    echo "✓ Search index is READY"
    exit 0
  fi

  if [[ ${attempt} -eq ${max_attempts} ]]; then
    echo "✗ ERROR: Search index not ready after ${max_attempts} attempts"
    exit 1
  fi

  sleep ${sleep_time}
done
