# Wait for vector search index to be ready (checking through mongos)
# For sharded clusters, the index needs to sync data from all shards which can take longer

echo "Waiting for vector search index to be ready..."

max_attempts=120
sleep_time=10

for attempt in $(seq 1 ${max_attempts}); do
  # Get the full index info for better debugging
  result=$(kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
    mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
mongosh "${MDB_CONNECTION_STRING}" --quiet --eval '
  const result = db.getSiblingDB("sample_mflix").runCommand({ listSearchIndexes: "embedded_movies" });
  if (result.ok && result.cursor && result.cursor.firstBatch) {
    const vectorIdx = result.cursor.firstBatch.find(idx => idx.name === "vector_index");
    if (vectorIdx) {
      // Check both status field and queryable field
      const status = vectorIdx.status || "READY";
      const queryable = vectorIdx.queryable !== undefined ? vectorIdx.queryable : true;
      print(JSON.stringify({ status: status, queryable: queryable }));
    } else {
      print(JSON.stringify({ status: "NO_INDEX", queryable: false }));
    }
  } else {
    print(JSON.stringify({ status: "NO_INDEX", queryable: false }));
  }
'
EOF
)" 2>&1 | grep -v "^Warning:" | grep -v "^Defaulted container" | tail -1)

  echo "Attempt ${attempt}/${max_attempts}: Vector index info = ${result}"

  # Parse the JSON result to check status
  status=$(echo "${result}" | grep -o '"status":"[^"]*"' | cut -d'"' -f4 || echo "UNKNOWN")
  queryable=$(echo "${result}" | grep -o '"queryable":[^,}]*' | cut -d':' -f2 || echo "false")

  if [[ "${status}" == "READY" ]] || [[ "${queryable}" == "true" ]]; then
    echo "✓ Vector search index is READY (status=${status}, queryable=${queryable})"
    exit 0
  fi

  if [[ ${attempt} -eq ${max_attempts} ]]; then
    echo "✗ ERROR: Vector search index not ready after ${max_attempts} attempts (status=${status})"
    exit 1
  fi

  sleep ${sleep_time}
done

