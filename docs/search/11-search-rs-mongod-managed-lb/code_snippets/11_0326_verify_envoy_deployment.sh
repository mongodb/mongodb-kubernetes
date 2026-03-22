#!/usr/bin/env bash
# Verify that the operator-managed Envoy proxy is deployed and running

echo "Verifying operator-managed Envoy deployment..."

envoy_deployment="${MDB_RESOURCE_NAME}-search-lb"
envoy_configmap="${MDB_RESOURCE_NAME}-search-lb-config"
lb_service="${MDB_RESOURCE_NAME}-search-lb-svc"

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

echo "Checking LB Service..."
if kubectl get service "${lb_service}" -n "${MDB_NS}" --context "${K8S_CTX}" &>/dev/null; then
  echo "  ✓ Service '${lb_service}' exists"
else
  echo "  ✗ Service '${lb_service}' NOT FOUND"
  exit 1
fi

echo ""
echo "Envoy pod status:"
kubectl get pods -n "${MDB_NS}" --context "${K8S_CTX}" -l "app=${envoy_deployment}"

echo ""
echo "✓ Operator-managed Envoy proxy is deployed and healthy"
