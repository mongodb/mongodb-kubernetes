#!/usr/bin/env bash
# Renders a helm install command pointing all image registries and versions to staging.
# Usage: ./scripts/render_helm_install.sh <version>
# Example: ./scripts/render_helm_install.sh 824ad793

set -euo pipefail

VERSION="${1:?Usage: $0 <version>}"

STAGING_REGISTRY="268558157000.dkr.ecr.us-east-1.amazonaws.com/staging"
CHART_OCI="oci://268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb/helm-charts/mongodb-kubernetes"
CHART_VERSION="0.0.0+${VERSION}"

cat <<EOF
helm install mongodb-kubernetes ${CHART_OCI} \\
  --version ${CHART_VERSION} \\
  --set registry.operator=${STAGING_REGISTRY} \\
  --set registry.database=${STAGING_REGISTRY} \\
  --set registry.initDatabase=${STAGING_REGISTRY} \\
  --set registry.initOpsManager=${STAGING_REGISTRY} \\
  --set registry.opsManager=${STAGING_REGISTRY} \\
  --set registry.agent=${STAGING_REGISTRY} \\
  --set registry.versionUpgradeHook=${STAGING_REGISTRY} \\
  --set registry.readinessProbe=${STAGING_REGISTRY} \\
  --set community.registry.agent=${STAGING_REGISTRY} \\
  --set operator.version=${VERSION} \\
  --set database.version=${VERSION} \\
  --set initDatabase.version=${VERSION} \\
  --set initOpsManager.version=${VERSION} \\
  --set agent.version=${VERSION} \\
  --set versionUpgradeHook.version=${VERSION} \\
  --set readinessProbe.version=${VERSION} \\
  --set community.agent.version=${VERSION} \\
  --set search.repo=${STAGING_REGISTRY} \\
  --set search.name=mongodb-search \\
  --set search.version=${VERSION}
EOF
