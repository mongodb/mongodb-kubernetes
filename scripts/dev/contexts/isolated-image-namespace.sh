#!/usr/bin/env bash

set -Eeou pipefail

# Opt-in isolation for agent and Ops Manager images.
#
# These images are tagged by agent/OM version (not by patch) and built with skip-if-exists, so a Dockerfile change in a
# patch is silently ignored when the tag exists, or pushed to a tag shared by every other patch and branch when it does
# not. When ISOLATED_IMAGE_NAMESPACE (or the Evergreen patch parameter `isolated_image_namespace`) is set, the pipeline
# builds them into "${REGISTRY}/<namespace>/<image>" (scripts/release/pipeline.sh) and this file points the
# operator and the tests at that namespace. With the variable unset this file is a no-op.
#
# Sourced by switch_context.sh after the variant context and before resolve-agent-version.sh.

ISOLATED_IMAGE_NAMESPACE="${ISOLATED_IMAGE_NAMESPACE:-${isolated_image_namespace:-}}"

if [[ -n "${ISOLATED_IMAGE_NAMESPACE}" ]]; then
  # Unset BUILD_SCENARIO means a local development context.
  if [[ -n "${BUILD_SCENARIO:-}" && "${BUILD_SCENARIO}" != "patch" && "${BUILD_SCENARIO}" != "development" ]]; then
    echo "isolated-image-namespace.sh: isolated images are only allowed for patch/development builds, BUILD_SCENARIO=${BUILD_SCENARIO:-}" >&2
    exit 1
  fi

  if [[ "${ISOLATED_IMAGE_NAMESPACE}" == "auto" ]]; then
    if [[ -z "${version_id:-}" ]]; then
      echo "isolated-image-namespace.sh: 'auto' requires the Evergreen version_id expansion" >&2
      exit 1
    fi
    ISOLATED_IMAGE_NAMESPACE="exp-${version_id}"
  fi

  if [[ ! "${ISOLATED_IMAGE_NAMESPACE}" =~ ^[a-z0-9]+([._-][a-z0-9]+)*$ ]]; then
    echo "isolated-image-namespace.sh: invalid namespace '${ISOLATED_IMAGE_NAMESPACE}'" >&2
    exit 1
  fi

  export ISOLATED_IMAGE_NAMESPACE
  export MDB_AGENT_REGISTRY="${REGISTRY}/${ISOLATED_IMAGE_NAMESPACE}"
  export MDB_AGENT_IMAGE_REPOSITORY="${MDB_AGENT_REGISTRY}/mongodb-agent"
  export OPS_MANAGER_REGISTRY="${REGISTRY}/${ISOLATED_IMAGE_NAMESPACE}"
  export OPS_MANAGER_IMAGE_REPOSITORY="${OPS_MANAGER_REGISTRY}/mongodb-enterprise-ops-manager-ubi"
  echo "Using isolated agent and Ops Manager images from ${REGISTRY}/${ISOLATED_IMAGE_NAMESPACE}"
else
  unset ISOLATED_IMAGE_NAMESPACE
fi
