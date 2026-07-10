#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes

cluster_name=$1
if [[ -z ${cluster_name} ]]; then
  echo "Usage: recreate_kind_cluster.sh <cluster_name>"
  exit 1
fi

if [[ "${DELETE_KIND_NETWORK:-"false"}" == "true" ]]; then
  delete_kind_network
fi

docker_create_kind_network
docker_run_local_registry "kind-registry" "5000"

# The kind node image is pulled from an ECR mirror (kubernetes-versions.json).
# EVG spawn hosts have no ambient ECR auth (the instance role can't
# GetAuthorizationToken, and any AMI-baked token is long expired), so log in
# explicitly using the AWS creds from the sourced dev context before
# setup_kind_cluster.sh triggers the node-image pull. No-op for non-ECR images.
kind_node_image="${KIND_NODE_IMAGE:-$(scripts/get-kind-image.sh max)}"
if [[ "${kind_node_image}" =~ ^([0-9]+\.dkr\.ecr\.([a-z0-9-]+)\.amazonaws\.com) ]]; then
  ecr_registry="${BASH_REMATCH[1]}"
  ecr_region="${BASH_REMATCH[2]}"
  if docker image inspect "${kind_node_image}" >/dev/null 2>&1; then
    # Already cached locally (the common case on a laptop) — no ECR round-trip
    # needed, so don't fail when AWS CLI is missing / SSO expired / offline.
    echo "Kind node image ${kind_node_image} already present locally; skipping ECR login"
  else
    echo "Logging in to ECR registry ${ecr_registry} (region ${ecr_region}) for kind node image"
    if ! aws ecr get-login-password --region "${ecr_region}" \
        | docker login --username AWS --password-stdin "${ecr_registry}"; then
      echo "WARN: ECR login failed; the node-image pull will fail unless the image is cached" >&2
    fi
  fi
fi

# shellcheck source=../funcs/kind_network
source scripts/funcs/kind_network
scripts/dev/setup_kind_cluster.sh -r -e -n "${cluster_name}" -l "${KIND_METALLB_RANGE_SINGLE}" -c "${CLUSTER_DOMAIN}"

source scripts/dev/install_csi_driver.sh
csi_driver_download
csi_driver_deploy "kind-${cluster_name}"

# Stamp the canonical per-side kubeconfig path so downstream tooling has
# one source of truth for "kind cluster was just created on this host" —
# the same artifact whether this runs on a laptop (local-kind) or an EVG
# runner (EVG-CI). Mirrors recreate_kind_clusters.sh's tail.
mkdir -p "${PROJECT_DIR}/.generated"
cp -f "${HOME}/.kube/${cluster_name}" "${PROJECT_DIR}/.generated/current.kubeconfig"
