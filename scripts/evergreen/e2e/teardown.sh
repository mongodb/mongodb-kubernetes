#!/usr/bin/env bash

set -Eeou pipefail

[[ "${MODE-}" = "dev" ]] && exit 0

context="${1}"

echo "Removing all CRs"
kubectl --context "${context}" delete mdb --all -n "${PROJECT_NAMESPACE}" || true
kubectl --context "${context}" delete mdbm --all -n "${PROJECT_NAMESPACE}" || true
kubectl --context "${context}" delete mdbu --all -n "${PROJECT_NAMESPACE}" || true
kubectl --context "${context}" delete om --all -n "${PROJECT_NAMESPACE}" || true

echo "Removing the HELM chart"
helm --kube-context "${context}" delete mongodb-enterprise-operator --namespace "${PROJECT_NAMESPACE}" || true

echo "Removing the test namespace ${PROJECT_NAMESPACE}"
kubectl --context "${context}" delete "namespace/${PROJECT_NAMESPACE}" --wait=false || true

echo "Removing CSRs"
kubectl --context "${context}" delete "$(kubectl get csr -o name | grep "${PROJECT_NAMESPACE}")" &> /dev/null || true
