#!/usr/bin/env bash

set -Eeou pipefail

# Resolve agent image vars from CUSTOM_OM_VERSION (release.json opsManagerMapping).
# Sourced after the variant context (sets CUSTOM_OM_VERSION) and before any
# private-context-override.

if [[ -z "${PROJECT_DIR:-}" ]]; then
  echo "resolve-agent-version.sh: PROJECT_DIR is not set" >&2
  exit 1
fi

if [[ -z "${MDB_AGENT_IMAGE_REPOSITORY:-}" ]]; then
  echo "resolve-agent-version.sh: MDB_AGENT_IMAGE_REPOSITORY is not set" >&2
  exit 1
fi

if [[ -n "${CUSTOM_OM_VERSION:-}" && "${CUSTOM_OM_VERSION}" != *-dev* ]]; then
  AGENT_VERSION="$(jq -r --arg om "${CUSTOM_OM_VERSION}" \
    '.supportedImages."mongodb-agent".opsManagerMapping.ops_manager[$om].agent_version // empty' \
    "${PROJECT_DIR}/release.json")"
  if [[ -z "${AGENT_VERSION}" ]]; then
    echo "resolve-agent-version.sh: no agent mapping for OM version ${CUSTOM_OM_VERSION} in release.json" >&2
    exit 1
  fi
else
  # Dev builds have no mapping entry and don't need one. AppDB starts before OM, so its agent image
  # is pinned to this resolved version, any current agent works there. OM managed deployments get
  # their agent version from the running OM, not from here.
  AGENT_VERSION="$(jq -r '.agentVersion' "${PROJECT_DIR}/release.json")"
fi

export AGENT_VERSION
export AGENT_IMAGE="${MDB_AGENT_IMAGE_REPOSITORY}:${AGENT_VERSION}"
export MDB_COMMUNITY_AGENT_IMAGE="${AGENT_IMAGE}"
