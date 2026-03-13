#!/usr/bin/env bash
# Installs the operator from staging registries.
# Usage: OPERATOR_VERSION=<short sha> SEARCH_VERSION=<short-sha> ./scripts/helm_install_staging.sh

set -euo pipefail
set -x
OPERATOR_VERSION="${OPERATOR_VERSION:?OPERATOR_VERSION env var is required}"
SEARCH_VERSION="${SEARCH_VERSION:?SEARCH_VERSION env var is required}"
IMAGE_PULL_SECRET="${IMAGE_PULL_SECRET:-mongodb-rapid-release-registry}"

STAGING_REGISTRY="quay.io/mongodb/staging"
SEARCH_RAPID_RELEASE_REGISTRY="901841024863.dkr.ecr.us-east-1.amazonaws.com"
CHART_OCI="oci://268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb/helm-charts/mongodb-kubernetes"
CHART_VERSION="0.0.0+${OPERATOR_VERSION}"

aws sso login --profile devprod-platforms-ecr-user

aws --profile devprod-platforms-ecr-user ecr get-login-password --region us-east-1 | docker login --username AWS --password-stdin 901841024863.dkr.ecr.us-east-1.amazonaws.com

# 0. Create image pull secret from ~/.docker/config.json credentials for the rapid-release ECR registry
CRED_HELPER=$(jq -r --arg reg "${SEARCH_RAPID_RELEASE_REGISTRY}" '.credHelpers[$reg] // .credsStore // empty' ~/.docker/config.json)
if [[ -n "${CRED_HELPER}" ]]; then
  CREDS=$(echo "${SEARCH_RAPID_RELEASE_REGISTRY}" | "docker-credential-${CRED_HELPER}" get)
  USERNAME=$(echo "${CREDS}" | jq -r '.Username')
  PASSWORD=$(echo "${CREDS}" | jq -r '.Secret')
else
  ENCODED_AUTH=$(jq -r --arg reg "${SEARCH_RAPID_RELEASE_REGISTRY}" '.auths[$reg].auth' ~/.docker/config.json)
  USERNAME=$(echo "${ENCODED_AUTH}" | base64 -d | cut -d: -f1)
  PASSWORD=$(echo "${ENCODED_AUTH}" | base64 -d | cut -d: -f2-)
fi

DOCKER_CONFIG_JSON=$(jq -n \
  --arg reg "${SEARCH_RAPID_RELEASE_REGISTRY}" \
  --arg auth "$(echo -n "${USERNAME}:${PASSWORD}" | base64)" \
  '{"auths": {($reg): {"auth": $auth}}}')

kubectl create secret generic "${IMAGE_PULL_SECRET}" \
  --from-literal=.dockerconfigjson="${DOCKER_CONFIG_JSON}" \
  --type=kubernetes.io/dockerconfigjson \
  --namespace "${NAMESPACE}" \
  --dry-run=client -o yaml | kubectl apply -f -

# 1. Pull and unpack the chart to extract CRDs
if [[ -d "./tmp/mongodb-kubernetes-chart-${OPERATOR_VERSION}/mongodb-kubernetes" ]]; then
  rm -rf "./tmp/mongodb-kubernetes-chart-${OPERATOR_VERSION}/mongodb-kubernetes"
fi
mkdir -p tmp/mongodb-kubernetes-chart-824ad793

helm pull "${CHART_OCI}" \
  --version "${CHART_VERSION}" \
  --untar --untardir ./tmp/mongodb-kubernetes-chart-"${OPERATOR_VERSION}"

# 2. Install CRDs directly with kubectl
kubectl apply -f ./tmp/mongodb-kubernetes-chart-"${OPERATOR_VERSION}"/mongodb-kubernetes/crds/

# 3. Install the operator (skip bundled CRD installation)
helm upgrade --install mongodb-kubernetes "${CHART_OCI}" \
  --version "${CHART_VERSION}" \
  --skip-crds \
  --namespace "${NAMESPACE}" \
  --create-namespace \
  --set telemetry.enabled=false \
  --set registry.operator="${STAGING_REGISTRY}" \
  --set registry.database="${STAGING_REGISTRY}" \
  --set registry.initDatabase="${STAGING_REGISTRY}" \
  --set registry.initOpsManager="${STAGING_REGISTRY}" \
  --set registry.opsManager="${STAGING_REGISTRY}" \
  --set registry.agent="${STAGING_REGISTRY}" \
  --set registry.versionUpgradeHook="${STAGING_REGISTRY}" \
  --set registry.readinessProbe="${STAGING_REGISTRY}" \
  --set community.registry.agent="${STAGING_REGISTRY}" \
  --set operator.version="${OPERATOR_VERSION}" \
  --set database.version="${OPERATOR_VERSION}" \
  --set initDatabase.version="${OPERATOR_VERSION}" \
  --set initOpsManager.version="${OPERATOR_VERSION}" \
  --set agent.version="${OPERATOR_VERSION}" \
  --set versionUpgradeHook.version="${OPERATOR_VERSION}" \
  --set readinessProbe.version="${OPERATOR_VERSION}" \
  --set community.agent.version="${OPERATOR_VERSION}" \
  --set search.repo="${SEARCH_RAPID_RELEASE_REGISTRY}/mongot-community" \
  --set search.name=rapid-releases \
  --set search.version="${SEARCH_VERSION}" \
  --set registry.imagePullSecrets="${IMAGE_PULL_SECRET}"

