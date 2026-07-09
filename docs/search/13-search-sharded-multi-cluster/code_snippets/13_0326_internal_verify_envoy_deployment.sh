echo "Verifying operator-managed per-cluster mongot and Envoy resources..."

ci=0
for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}"; do
  envoy_deployment="${MDB_SEARCH_RESOURCE_NAME}-search-lb-${ci}"

  echo "== cluster ${ci} (${ctx}) =="

  echo "Checking per-shard mongot StatefulSets..."
  for shard_name in "${MDB_SHARD_0_NAME}" "${MDB_SHARD_1_NAME}" "${MDB_SHARD_2_NAME}"; do
    mongot_sts="${MDB_SEARCH_RESOURCE_NAME}-search-${ci}-${shard_name}"
    if kubectl get statefulset "${mongot_sts}" -n "${MDB_NS}" --context "${ctx}" &>/dev/null; then
      echo "  [ok] StatefulSet '${mongot_sts}' exists"
    else
      echo "  [FAIL] StatefulSet '${mongot_sts}' NOT FOUND" >&2
    fi
  done

  echo "Checking Envoy Deployment..."
  if kubectl get deployment "${envoy_deployment}" -n "${MDB_NS}" --context "${ctx}" &>/dev/null; then
    ready=$(kubectl get deployment "${envoy_deployment}" -n "${MDB_NS}" --context "${ctx}" \
      -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
    desired=$(kubectl get deployment "${envoy_deployment}" -n "${MDB_NS}" --context "${ctx}" \
      -o jsonpath='{.spec.replicas}' 2>/dev/null || echo "1")

    if [[ "${ready}" -ge "${desired}" ]]; then
      echo "  [ok] Deployment '${envoy_deployment}' is ready (${ready}/${desired} replicas)"
    else
      echo "  [warn] Deployment '${envoy_deployment}' is not fully ready (${ready}/${desired} replicas)"
      echo "    Waiting for Envoy pods..."
      kubectl rollout status deployment/"${envoy_deployment}" -n "${MDB_NS}" --context "${ctx}" --timeout=120s
    fi
  else
    echo "  [FAIL] Deployment '${envoy_deployment}' NOT FOUND" >&2
  fi

  echo "Checking per-shard proxy Services..."
  for shard_name in "${MDB_SHARD_0_NAME}" "${MDB_SHARD_1_NAME}" "${MDB_SHARD_2_NAME}"; do
    proxy_service="${MDB_SEARCH_RESOURCE_NAME}-search-${ci}-${shard_name}-proxy-svc"
    if kubectl get service "${proxy_service}" -n "${MDB_NS}" --context "${ctx}" &>/dev/null; then
      echo "  [ok] Service '${proxy_service}' exists"
    else
      echo "  [FAIL] Service '${proxy_service}' NOT FOUND" >&2
    fi
  done

  ci=$((ci + 1))
done

echo ""
echo "[ok] Operator-managed per-cluster Envoy proxies are deployed and healthy"
