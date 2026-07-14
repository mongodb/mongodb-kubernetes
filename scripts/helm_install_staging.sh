#!/usr/bin/env bash
# Installs the operator from staging registries.
# Usage: OPERATOR_VERSION=<short sha> SEARCH_VERSION=<short-sha> ./scripts/helm_install_staging.sh

set -euo pipefail

# Prints a command (with optional redactions) and then executes it.
# Usage: run_cmd <command...>
#        run_cmd --display "visible form" -- <command...>
run_cmd() {
  local display_cmd=""
  if [[ "${1:-}" == "--display" ]]; then
    shift; display_cmd="$1"; shift
    [[ "${1:-}" == "--" ]] && shift
  else
    display_cmd="$*"
  fi
  echo "    \$ ${display_cmd}"
  "$@"
}

OPERATOR_VERSION="${OPERATOR_VERSION:?OPERATOR_VERSION env var is required}"
SEARCH_VERSION="${SEARCH_VERSION:?SEARCH_VERSION env var is required}"
NAMESPACE="${NAMESPACE:?NAMESPACE env var is required}"
IMAGE_PULL_SECRET="${IMAGE_PULL_SECRET:-mongodb-rapid-release-registry}"

staging_registry="quay.io/mongodb/staging"
search_rapid_release_registry="901841024863.dkr.ecr.us-east-1.amazonaws.com"
chart_oci="oci://quay.io/mongodb/staging/helm-charts/mongodb-kubernetes"
chart_version="0.0.0+${OPERATOR_VERSION}"
aws_profile="devprod-platforms-ecr-user"
kube_context="$(kubectl config current-context)"

cat <<EOF
================================================================================
This script will perform the following steps:

  1. Log in to AWS using SSO profile '${aws_profile}'.
  2. Obtain an ECR auth token from AWS and use it to docker-login to the
     rapid-releases registry '${search_rapid_release_registry}'.
  3. Read Docker credentials from ~/.docker/config.json for the registry
     '${search_rapid_release_registry}' and create a Kubernetes
     imagePullSecret '${NAMESPACE}/${IMAGE_PULL_SECRET}'
     (type: kubernetes.io/dockerconfigjson).
  4. Pull the Helm chart '${chart_oci}' version '${chart_version}'
     and install CRDs from it into the cluster.
  5. Install (or upgrade) the mongodb-kubernetes operator via Helm into
     namespace '${NAMESPACE}', with operator image '${staging_registry}/mongodb-kubernetes:${OPERATOR_VERSION}'
     and search image '${search_rapid_release_registry}/mongot-community/rapid-releases:${SEARCH_VERSION}'.

  K8s context      : ${kube_context}
  Operator image   : ${staging_registry}/mongodb-kubernetes:${OPERATOR_VERSION}
  Search image     : ${search_rapid_release_registry}/mongot-community/rapid-releases:${SEARCH_VERSION}
  Target namespace : ${NAMESPACE}
================================================================================
EOF

read -r -p "Do you want to continue? [y/N] " response
if [[ ! "${response}" =~ ^[Yy]$ ]]; then
  echo "Aborted."
  exit 0
fi

# ---------------------------------------------------------------------------
# Step 1: AWS SSO login
# ---------------------------------------------------------------------------
echo ""
echo ">>> Step 1/5: Logging in to AWS SSO (profile: ${aws_profile})..."
run_cmd aws sso login --profile "${aws_profile}"
echo "    AWS SSO login complete."

# ---------------------------------------------------------------------------
# Step 2: Docker login to ECR rapid-releases registry
# ---------------------------------------------------------------------------
echo ""
echo ">>> Step 2/5: Obtaining ECR auth token and logging Docker into '${search_rapid_release_registry}'..."
echo "    \$ aws --profile ${aws_profile} ecr get-login-password --region us-east-1 | docker login --username AWS --password-stdin ${search_rapid_release_registry}"
aws --profile "${aws_profile}" ecr get-login-password --region us-east-1 \
  | docker login --username AWS --password-stdin "${search_rapid_release_registry}"
echo "    Docker login to ECR complete."

# ---------------------------------------------------------------------------
# Step 3: Create imagePullSecret in Kubernetes
# ---------------------------------------------------------------------------
echo ""
echo ">>> Step 3/5: Creating imagePullSecret '${IMAGE_PULL_SECRET}' in namespace '${NAMESPACE}' (context: ${kube_context})..."
echo "    Source: Docker credentials from ~/.docker/config.json for '${search_rapid_release_registry}'."

cred_helper=$(jq -r --arg reg "${search_rapid_release_registry}" '.credHelpers[$reg] // .credsStore // empty' ~/.docker/config.json)
if [[ -n "${cred_helper}" ]]; then
  echo "    Using Docker credential helper: docker-credential-${cred_helper}"
  creds=$(echo "${search_rapid_release_registry}" | "docker-credential-${cred_helper}" get)
  username=$(echo "${creds}" | jq -r '.Username')
  password=$(echo "${creds}" | jq -r '.Secret')
else
  echo "    Using base64-encoded auth from ~/.docker/config.json"
  encoded_auth=$(jq -r --arg reg "${search_rapid_release_registry}" '.auths[$reg].auth' ~/.docker/config.json)
  username=$(echo "${encoded_auth}" | base64 -d | cut -d: -f1)
  password=$(echo "${encoded_auth}" | base64 -d | cut -d: -f2-)
fi

docker_config_json=$(jq -n \
  --arg reg "${search_rapid_release_registry}" \
  --arg auth "$(echo -n "${username}:${password}" | base64)" \
  '{"auths": {($reg): {"auth": $auth}}}')

echo "    \$ kubectl --context ${kube_context} create secret generic ${IMAGE_PULL_SECRET} --from-literal=.dockerconfigjson=<REDACTED> --type=kubernetes.io/dockerconfigjson --namespace ${NAMESPACE} --dry-run=client -o yaml | kubectl apply -f -"
kubectl --context "${kube_context}" create secret generic "${IMAGE_PULL_SECRET}" \
  --from-literal=.dockerconfigjson="${docker_config_json}" \
  --type=kubernetes.io/dockerconfigjson \
  --namespace "${NAMESPACE}" \
  --dry-run=client -o yaml | kubectl --context "${kube_context}" apply -f -
echo "    K8s secret '${NAMESPACE}/${IMAGE_PULL_SECRET}' created/updated."

# ---------------------------------------------------------------------------
# Step 4: Pull Helm chart and install CRDs
# ---------------------------------------------------------------------------
echo ""
echo ">>> Step 4/5: Pulling Helm chart '${chart_oci}' (version: ${chart_version}) and installing CRDs (context: ${kube_context})..."

if [[ -d "./tmp/mongodb-kubernetes-chart-${OPERATOR_VERSION}/mongodb-kubernetes" ]]; then
  echo "    Removing previously unpacked chart..."
  rm -rf "./tmp/mongodb-kubernetes-chart-${OPERATOR_VERSION}/mongodb-kubernetes"
fi
mkdir -p tmp/mongodb-kubernetes-chart-"${OPERATOR_VERSION}"

run_cmd helm pull "${chart_oci}" \
  --version "${chart_version}" \
  --untar --untardir ./tmp/mongodb-kubernetes-chart-"${OPERATOR_VERSION}"
echo "    Chart unpacked to ./tmp/mongodb-kubernetes-chart-${OPERATOR_VERSION}/mongodb-kubernetes"

run_cmd kubectl --context "${kube_context}" apply -f ./tmp/mongodb-kubernetes-chart-"${OPERATOR_VERSION}"/mongodb-kubernetes/crds/
echo "    CRDs installed."

# ---------------------------------------------------------------------------
# Step 5: Install / upgrade the operator via Helm
# ---------------------------------------------------------------------------
echo ""
echo ">>> Step 5/5: Installing mongodb-kubernetes operator into namespace '${NAMESPACE}' (context: ${kube_context})..."
echo "    Operator image  : ${staging_registry}/mongodb-kubernetes:${OPERATOR_VERSION}"
echo "    Search image    : ${search_rapid_release_registry}/mongot-community/rapid-releases:${SEARCH_VERSION}"
echo "    imagePullSecret : ${IMAGE_PULL_SECRET}"

run_cmd helm --kube-context "${kube_context}" upgrade --install mongodb-kubernetes-operator "${chart_oci}" \
  --version "${chart_version}" \
  --skip-crds \
  --namespace "${NAMESPACE}" \
  --create-namespace \
  --set telemetry.enabled=false \
  --set registry.operator="${staging_registry}" \
  --set registry.database="${staging_registry}" \
  --set registry.initDatabase="${staging_registry}" \
  --set registry.initOpsManager="${staging_registry}" \
  --set registry.opsManager="${staging_registry}" \
  --set registry.agent="${staging_registry}" \
  --set registry.versionUpgradeHook="${staging_registry}" \
  --set registry.readinessProbe="${staging_registry}" \
  --set community.registry.agent="${staging_registry}" \
  --set operator.version="${OPERATOR_VERSION}" \
  --set database.version="${OPERATOR_VERSION}" \
  --set initDatabase.version="${OPERATOR_VERSION}" \
  --set initOpsManager.version="${OPERATOR_VERSION}" \
  --set agent.version="${OPERATOR_VERSION}" \
  --set versionUpgradeHook.version="${OPERATOR_VERSION}" \
  --set readinessProbe.version="${OPERATOR_VERSION}" \
  --set community.agent.version="${OPERATOR_VERSION}" \
  --set search.repo="${search_rapid_release_registry}/mongot-community" \
  --set search.name=rapid-releases \
  --set search.version="${SEARCH_VERSION}" \
  --set registry.imagePullSecrets="${IMAGE_PULL_SECRET}"

echo ""
echo ">>> Done! Operator installed successfully in namespace '${NAMESPACE}'."
