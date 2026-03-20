echo "Waiting for MongoDBSearch to be ready..."
echo "This may take several minutes while mongot syncs data..."

timeout=600  # 10 minutes
interval=10
elapsed=0

while [[ ${elapsed} -lt ${timeout} ]]; do
  phase=$(kubectl get mongodbsearch "${MDB_SEARCH_RESOURCE_NAME}" \
    -n "${MDB_NS}" \
    --context "${K8S_CTX}" \
    -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")

  if [[ "${phase}" == "Running" ]]; then
    echo "✓ MongoDBSearch is Running"
    break
  fi

  ready_mongots=$(kubectl get pods \
    -n "${MDB_NS}" --context "${K8S_CTX}" \
    -l "app.kubernetes.io/component=mongot" \
    -o jsonpath='{range .items[*]}{.status.containerStatuses[0].ready}{"\n"}{end}' \
    2>/dev/null | grep -c "true" || echo "0")

  echo "  Phase: ${phase}" \
    "| Ready mongot pods: ${ready_mongots}" \
    "(${elapsed}s/${timeout}s)"
  sleep ${interval}
  elapsed=$((elapsed + interval))
done

if [[ ${elapsed} -ge ${timeout} ]]; then
  echo "ERROR: Timeout waiting for MongoDBSearch to be ready"
  echo ""
  echo "MongoDBSearch status:"
  kubectl describe mongodbsearch \
    "${MDB_SEARCH_RESOURCE_NAME}" \
    -n "${MDB_NS}" --context "${K8S_CTX}"
  echo ""
  echo "Search pods:"
  kubectl get pods -n "${MDB_NS}" \
    --context "${K8S_CTX}" \
    | grep -E "search|mongot"
  exit 1
fi

echo ""
echo "MongoDBSearch pods:"
kubectl get pods -n "${MDB_NS}" \
  --context "${K8S_CTX}" \
  -l "app.kubernetes.io/component=mongot"

echo ""
echo "✓ MongoDBSearch '${MDB_SEARCH_RESOURCE_NAME}' is ready"
