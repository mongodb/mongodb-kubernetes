#!/usr/bin/env bash
# Add the MongoDB Helm repository
#
# The MongoDB Kubernetes Operator is distributed via Helm charts.
# This adds the official MongoDB Helm repository.

helm repo add mongodb https://mongodb.github.io/helm-charts 2>/dev/null || true
helm repo update mongodb

echo "✓ MongoDB Helm repository added and updated"

