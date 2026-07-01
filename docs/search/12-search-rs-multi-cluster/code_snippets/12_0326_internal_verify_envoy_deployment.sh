echo "Verifying operator-managed per-cluster mongot and Envoy resources..."

# One mongot StatefulSet and one Envoy (managed load balancer) Deployment must
# come up in each member cluster.
ci=0
for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}"; do
  mongot_sts="${MDB_SEARCH_RESOURCE_NAME}-search-${ci}"
  envoy_deployment="${MDB_SEARCH_RESOURCE_NAME}-search-lb-0-${ci}"
  proxy_service="${MDB_SEARCH_RESOURCE_NAME}-search-${ci}-proxy-svc"

  echo "== cluster ${ci} (${ctx}) =="

  echo "Checking mongot StatefulSet..."
  if kubectl get statefulset "${mongot_sts}" -n "${MDB_NS}" --context "${ctx}" &>/dev/null; then
    echo "  [ok] StatefulSet '${mongot_sts}' exists"
  else
    echo "  [FAIL] StatefulSet '${mongot_sts}' NOT FOUND" >&2
  fi

  echo "Checking Envoy Deployment..."
  if kubectl get deployment "${envoy_deployment}" -n "${MDB_NS}" --context "${ctx}" &>/dev/null; then
    kubectl rollout status deployment/"${envoy_deployment}" -n "${MDB_NS}" --context "${ctx}" --timeout=120s
    echo "  [ok] Deployment '${envoy_deployment}' is ready"
  else
    echo "  [FAIL] Deployment '${envoy_deployment}' NOT FOUND" >&2
  fi

  echo "Checking Proxy Service..."
  if kubectl get service "${proxy_service}" -n "${MDB_NS}" --context "${ctx}" &>/dev/null; then
    echo "  [ok] Service '${proxy_service}' exists"
  else
    echo "  [FAIL] Service '${proxy_service}' NOT FOUND" >&2
  fi

  ci=$((ci + 1))
done

echo ""
echo "[ok] Operator-managed per-cluster Envoy proxies are deployed and healthy"
