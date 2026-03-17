#!/usr/bin/env bash
# Wait for search indexes to be ready

echo "Waiting for search indexes to be ready..."
echo "This may take several minutes depending on data size..."

user_conn="${MDB_USER_CONNECTION_STRING:-${MDB_CONNECTION_STRING}}"

timeout=300  # 5 minutes
interval=10
elapsed=0

while [[ ${elapsed} -lt ${timeout} ]]; do
  # shellcheck disable=SC2016
  text_status=$(kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
    mongosh "${user_conn}" --quiet --eval '
      const indexes = db.getSiblingDB("sample_mflix").movies.aggregate([
        { $listSearchIndexes: { name: "default" } }
      ]).toArray();
      if (indexes.length > 0) {
        print(indexes[0].status);
      } else {
        print("NOT_FOUND");
      }
    ' 2>/dev/null || echo "ERROR")

  # shellcheck disable=SC2016
  vector_status=$(kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- \
    mongosh "${user_conn}" --quiet --eval '
      const indexes = db.getSiblingDB("sample_mflix").embedded_movies.aggregate([
        { $listSearchIndexes: { name: "vector_index" } }
      ]).toArray();
      if (indexes.length > 0) {
        print(indexes[0].status);
      } else {
        print("NOT_FOUND");
      }
    ' 2>/dev/null || echo "SKIPPED")

  echo "  Text index: ${text_status} | Vector index: ${vector_status} (${elapsed}s/${timeout}s)"

  if [[ "${text_status}" == "READY" ]]; then
    echo ""
    echo "Text search index is READY"
    if [[ "${vector_status}" == "READY" ]]; then
      echo "Vector search index is READY"
    elif [[ "${vector_status}" != "NOT_FOUND" ]] && [[ "${vector_status}" != "SKIPPED" ]]; then
      echo "Vector search index still building: ${vector_status}"
    fi
    exit 0
  fi

  sleep ${interval}
  elapsed=$((elapsed + interval))
done

echo ""
echo "Timeout waiting for search indexes"
echo "The indexes may still be building. You can check status manually:"
echo "  kubectl exec mongodb-tools -n ${MDB_NS} -- mongosh '${user_conn}' --eval 'db.getSiblingDB(\"sample_mflix\").movies.aggregate([{\$listSearchIndexes: {}}])'"
exit 1
