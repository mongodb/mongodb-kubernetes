user_conn="${MDB_USER_CONNECTION_STRING:-${MDB_CONNECTION_STRING}}"
max_attempts=30
sleep_time=10

echo "Waiting for search indexes to become READY" \
  "(up to $((max_attempts * sleep_time))s)..."

get_index_status() {
  local collection="$1"
  local index_name="$2"

  kubectl exec mongodb-tools \
    -n "${MDB_NS}" \
    --context "${K8S_CTX}" \
    -- mongosh "${user_conn}" --quiet --eval "
  const result = db.getSiblingDB(\"sample_mflix\").runCommand({ listSearchIndexes: \"${collection}\" });
  if (result.ok && result.cursor && result.cursor.firstBatch && result.cursor.firstBatch.length > 0) {
    const idx = result.cursor.firstBatch.find(i => i.name === \"${index_name}\");
    print(idx ? (idx.status || \"UNKNOWN\") : \"NOT_FOUND\");
  } else {
    print(\"NOT_FOUND\");
  }
  " 2>/dev/null | tail -1
}

wait_for_index_ready() {
  local collection="$1"
  local index_name="$2"

  echo "Waiting for index '${index_name}' on collection '${collection}'..."
  for attempt in $(seq 1 "${max_attempts}"); do
    status="$(get_index_status "${collection}" "${index_name}")"
    echo "Attempt ${attempt}/${max_attempts}: status='${status}'"

    if [[ "${status}" == "READY" ]]; then
      echo "Index '${index_name}' is READY"
      return 0
    fi

    sleep "${sleep_time}"
  done

  echo "ERROR: Index '${index_name}' did not become READY " \
    "after $((max_attempts * sleep_time)) seconds" >&2
  return 1
}

wait_for_index_ready "movies" "default"
wait_for_index_ready "embedded_movies" "vector_index"

echo "All required search indexes are READY"
