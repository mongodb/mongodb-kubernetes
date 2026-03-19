#!/usr/bin/env bash
# Verify the MongoDB Helm chart is accessible via OCI registry
# No 'helm repo add' needed — OCI charts are pulled directly.

helm show chart "${OPERATOR_HELM_CHART}"
