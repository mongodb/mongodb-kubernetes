#!/usr/bin/env bash
# Add the MongoDB Helm repository

helm show chart "${OPERATOR_HELM_CHART}"

echo "✓ MongoDB Helm chart is accessible"
