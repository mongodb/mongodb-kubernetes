#!/usr/bin/env bash
# Install the MCK operator on every cluster listed in $CLUSTERS using the
# kubeconfig context names as cluster identifiers. Assumes the chart is
# already available locally and that raft-* ConfigMaps were provisioned by
# `kubectl-mongodb multicluster setup`.

set -euo pipefail

: "${CLUSTERS:?CLUSTERS env var must be set, e.g. CLUSTERS='central,m1,m2'}"
: "${NAMESPACE:?NAMESPACE env var must be set, e.g. NAMESPACE=mongodb}"
: "${CHART:?CHART env var must be set, e.g. CHART=helm_chart}"
: "${OPERATOR_NAME:=mongodb-kubernetes-operator}"

IFS=',' read -ra CTXS <<< "$CLUSTERS"
for ctx in "${CTXS[@]}"; do
  echo "==> Installing operator on $ctx"
  helm upgrade --install --kube-context "$ctx" "$OPERATOR_NAME" "$CHART" \
    --namespace "$NAMESPACE" \
    --create-namespace
done
