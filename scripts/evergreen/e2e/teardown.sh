#!/usr/bin/env bash

set -Eeou pipefail

[[ "${MODE-}" = "dev" ]] && exit 0

context="${1}"

echo "Removing all CRs"
kubectl --context "${context}" delete mdb --all -n "${NAMESPACE}" || true
kubectl --context "${context}" delete mdbmc --all -n "${NAMESPACE}" || true
kubectl --context "${context}" delete mdbu --all -n "${NAMESPACE}" || true
kubectl --context "${context}" delete om --all -n "${NAMESPACE}" || true

echo "Removing the HELM chart"
helm --kube-context "${context}" delete mongodb-enterprise-operator --namespace "${NAMESPACE}" || true

echo "Removing the test namespace ${NAMESPACE}"
kubectl --context "${context}" delete "namespace/${NAMESPACE}" --wait=false || true

echo "Removing CSRs"
kubectl --context "${context}" delete "$(kubectl get csr -o name | grep "${NAMESPACE}")" &> /dev/null || true
