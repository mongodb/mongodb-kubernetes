#!/usr/bin/env bash
# AUDIENCE: internal
# Add the MongoDB Helm repository

helm repo add mongodb https://mongodb.github.io/helm-charts 2>/dev/null || true
helm repo update mongodb

echo "✓ MongoDB Helm repository added and updated"

