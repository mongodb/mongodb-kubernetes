#!/usr/bin/env bash
# Verify that the operator-managed Envoy proxy is deployed and running

echo "Verifying operator-managed Envoy deployment..."

envoy_deployment="${MDB_SEARCH_RESOURCE_NAME}-search-lb"
envoy_configmap="${MDB_SEARCH_RESOURCE_NAME}-search-lb-config"

echo "Checking Envoy ConfigMap..."
if kubectl get configmap "${envoy_configmap}" -n "${MDB_NS}" --context "${K8S_CTX}" &>/dev/null; then
  echo "  ✓ ConfigMap '${envoy_configmap}' exists"
else
  echo "  ✗ ConfigMap '${envoy_configmap}' NOT FOUND"
  exit 1
fi

echo "Checking Envoy Deployment..."
if kubectl get deployment "${envoy_deployment}" -n "${MDB_NS}" --context "${K8S_CTX}" &>/dev/null; then
  ready=$(kubectl get deployment "${envoy_deployment}" -n "${MDB_NS}" --context "${K8S_CTX}" \
    -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
  desired=$(kubectl get deployment "${envoy_deployment}" -n "${MDB_NS}" --context "${K8S_CTX}" \
    -o jsonpath='{.spec.replicas}' 2>/dev/null || echo "1")

  if [[ "${ready}" -ge "${desired}" ]]; then
    echo "  ✓ Deployment '${envoy_deployment}' is ready (${ready}/${desired} replicas)"
  else
    echo "  ⚠ Deployment '${envoy_deployment}' is not fully ready (${ready}/${desired} replicas)"
    echo "    Waiting for Envoy pods..."
    kubectl rollout status deployment/"${envoy_deployment}" -n "${MDB_NS}" --context "${K8S_CTX}" --timeout=120s
  fi
else
  echo "  ✗ Deployment '${envoy_deployment}' NOT FOUND"
  exit 1
fi

echo "Checking per-shard proxy Services..."
for ((shard = 0; shard < MDB_SHARD_COUNT; shard++)); do
  shard_name="${MDB_EXTERNAL_CLUSTER_NAME}-${shard}"
  proxy_svc="${MDB_SEARCH_RESOURCE_NAME}-search-0-${shard_name}-proxy-svc"

  if kubectl get service "${proxy_svc}" -n "${MDB_NS}" --context "${K8S_CTX}" &>/dev/null; then
    echo "  ✓ Service '${proxy_svc}' exists"
  else
    echo "  ✗ Service '${proxy_svc}' NOT FOUND"
    exit 1
  fi
done

echo ""
echo "Envoy pod status:"
kubectl get pods -n "${MDB_NS}" --context "${K8S_CTX}" -l "app=${envoy_deployment}"

echo ""
echo "✓ Operator-managed Envoy proxy is deployed and healthy"
