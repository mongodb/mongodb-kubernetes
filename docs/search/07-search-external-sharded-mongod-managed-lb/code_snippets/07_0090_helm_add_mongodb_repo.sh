#!/usr/bin/env bash
# Add the MongoDB Helm repository

helm repo add mongodb https://mongodb.github.io/helm-charts 2>/dev/null || true
helm repo update mongodb
helm search repo mongodb/mongodb-kubernetes
