#!/usr/bin/env bash
# Wait for MongoDBSearch resource to reach Running phase
#
# This waits for:
# 1. All mongot StatefulSets to have ready replicas
# 2. The MongoDBSearch resource to report Running status
#
# The operator also deploys Envoy during this time.
#
# ============================================================================
# EXPECTED TIMELINE
# ============================================================================
# - 1-3 minutes: mongot pods start, connect to MongoDB
# - 3-10 minutes: Initial data sync (depends on data size)
# - Status progression: Unknown → Pending → Running
#
# If this times out, common issues are:
# - TLS certificates missing (run 07_0316a and 07_0316b first)
# - Password secret missing for search-sync-source user
# - MongoDB cluster not ready or unreachable
# ============================================================================
# DEPENDS ON: 07_0320_create_mongodb_search_resource.sh
# ============================================================================

echo "Waiting for MongoDBSearch to be ready..."
echo "This may take several minutes while mongot syncs data..."
echo ""

timeout=600  # 10 minutes
interval=10
elapsed=0

while [[ $elapsed -lt $timeout ]]; do
  phase=$(kubectl get mongodbsearch "${MDB_SEARCH_RESOURCE_NAME}" \
    -n "${MDB_NS}" \
    --context "${K8S_CTX}" \
    -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")

  if [[ "$phase" == "Running" ]]; then
    echo "✓ MongoDBSearch is Running"
    break
  fi

  ready_mongots=$(kubectl get pods -n "${MDB_NS}" --context "${K8S_CTX}" \
    -l "app.kubernetes.io/component=mongot" \
    -o jsonpath='{range .items[*]}{.status.containerStatuses[0].ready}{"\n"}{end}' 2>/dev/null | grep -c "true" || echo "0")

  echo "  Phase: ${phase} | Ready mongot pods: ${ready_mongots} (${elapsed}s/${timeout}s)"
  sleep $interval
  elapsed=$((elapsed + interval))
done

if [[ $elapsed -ge $timeout ]]; then
  echo "ERROR: Timeout waiting for MongoDBSearch to be ready"
  echo ""
  echo "MongoDBSearch status:"
  kubectl describe mongodbsearch "${MDB_SEARCH_RESOURCE_NAME}" -n "${MDB_NS}" --context "${K8S_CTX}"
  echo ""
  echo "Search pods:"
  kubectl get pods -n "${MDB_NS}" --context "${K8S_CTX}" | grep -E "search|mongot"
  exit 1
fi

# Show final status
echo ""
echo "MongoDBSearch pods:"
kubectl get pods -n "${MDB_NS}" --context "${K8S_CTX}" -l "app.kubernetes.io/component=mongot"

echo ""
echo "✓ MongoDBSearch '${MDB_SEARCH_RESOURCE_NAME}' is ready"
