#!/usr/bin/env bash

set -Eeou pipefail

[[ "${MODE-}" = "dev" ]] && exit 0

echo "Removing all CRs"
kubectl delete mdb --all -n "${PROJECT_NAMESPACE}" || true
kubectl delete mdbu --all -n "${PROJECT_NAMESPACE}" || true
kubectl delete om --all -n "${PROJECT_NAMESPACE}" || true

echo "Removing the HELM chart"
helm delete mongodb-enterprise-operator --namespace "${PROJECT_NAMESPACE}" || true

echo "Removing the test namespace ${PROJECT_NAMESPACE}"
kubectl delete "namespace/${PROJECT_NAMESPACE}" --wait=false || true

echo "Removing CSRs"
kubectl delete "$(kubectl get csr -o name | grep "${PROJECT_NAMESPACE}")" &> /dev/null || true
