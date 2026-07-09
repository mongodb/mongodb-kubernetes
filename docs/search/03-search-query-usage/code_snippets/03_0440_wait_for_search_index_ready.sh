max_attempts=30
sleep_time=10
max_wait_seconds=$((max_attempts * sleep_time))

echo "Waiting for search indexes to be query-ready " \
  "(up to ${max_wait_seconds}s)..."

get_index_status() {
  local collection="$1"
  local index_name="$2"

  kubectl exec --context "${K8S_CTX}" -n "${MDB_NS}" mongodb-tools-pod \
    -- mongosh "${MDB_CONNECTION_STRING}" --quiet --eval "
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
  local display_name="$3"
  local status=""

  echo "Checking ${display_name}..."
  for attempt in $(seq 1 "${max_attempts}"); do
    status="$(get_index_status "${collection}" "${index_name}")"

    if [[ "${status}" == "READY" ]]; then
      echo "Ready: ${display_name}"
      return 0
    fi

    if (( attempt == 1 || attempt % 3 == 0 || attempt == max_attempts )); then
      echo "Still building ${display_name} (${attempt}/${max_attempts})..."
    fi

    sleep "${sleep_time}"
  done

  echo "ERROR: Timed out waiting for ${display_name} after ${max_wait_seconds}s." >&2
  echo "Check snippet outputs 03_0444, 03_0445, and 03_0447 for index status." >&2
  return 1
}

wait_for_index_ready "movies" "default" "text search index (movies/default)"
wait_for_index_ready "embedded_movies" "vector_index" \
  "vector search index (embedded_movies/vector_index)"

if [[ -n "${EMBEDDING_MODEL:-}" ]]; then
  wait_for_index_ready "movies" "vector_auto_embed_index" \
    "auto-embed vector index (movies/vector_auto_embed_index)"
fi

echo "All required search indexes are ready. Continue to query snippets."
