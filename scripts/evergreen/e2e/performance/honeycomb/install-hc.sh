#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes

# Set the namespace from the environment variable
NAMESPACE=${NAMESPACE:-default}
BUILD_VARIANT=${BUILD_VARIANT:-default}
task_id=${task_id:-default}
version_id=${version_id:-default}
task_name=${task_name:-default}
otel_collector_endpoint=${otel_collector_endpoint:-default}

ensure_namespace "${NAMESPACE}"

# Apply the Service configuration
cat <<EOF | kubectl apply -f -
kind: Service
apiVersion: v1
metadata:
  name: mongodb-enterprise-operator
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/name: mongodb-enterprise-operator
    app.kubernetes.io/instance: monitoring
spec:
  selector:
    app.kubernetes.io/name: mongodb-enterprise-operator
  ports:
    - name: metric
      port: 8080
EOF


helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo update
kubectl create namespace honeycomb --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret generic honeycomb --from-literal=endpoint="${otel_collector_endpoint}" --namespace=honeycomb --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret generic namespace --from-literal=namespace="${NAMESPACE}" --namespace=honeycomb --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret generic build-variant --from-literal=build-variant="${BUILD_VARIANT}"  --namespace=honeycomb --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret generic version-id --from-literal=version-id="${version_id}"  --namespace=honeycomb --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret generic task-id --from-literal=task-id="${task_id}" --namespace=honeycomb --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret generic task-name --from-literal=task-name="${task_name}" --namespace=honeycomb --dry-run=client -o yaml | kubectl apply -f -

# The otel-collector Helm charts create cluster-scoped resources (ClusterRoles) so they must be
# installed into a stable shared namespace. With max_hosts>1 two tasks may run concurrently on the
# same cluster, causing Helm lock conflicts. Recovery steps:
#   1. Roll back any release stuck in a pending-* state (left by a previous failed task).
#   2. Use || true so a concurrent install by another task does not fail this task — the collector
#      will already be running or will be started by the other task.
for _release in otel-collector-cluster otel-collector; do
    _status=$(helm status "${_release}" --namespace honeycomb -o json 2>/dev/null \
        | jq -r '.info.status // empty' 2>/dev/null || true)
    if [[ "${_status}" == "pending-install" || "${_status}" == "pending-upgrade" || "${_status}" == "pending-rollback" ]]; then
        echo "Recovering stuck Helm release ${_release} (status: ${_status})"
        helm rollback "${_release}" --namespace honeycomb 2>/dev/null \
            || helm uninstall "${_release}" --namespace honeycomb 2>/dev/null \
            || true
    fi
done

helm upgrade --install otel-collector-cluster open-telemetry/opentelemetry-collector --namespace honeycomb --values scripts/evergreen/e2e/performance/honeycomb/values-deployment.yaml || true
helm upgrade --install otel-collector open-telemetry/opentelemetry-collector --namespace honeycomb --values scripts/evergreen/e2e/performance/honeycomb/values-daemonset.yaml || true
