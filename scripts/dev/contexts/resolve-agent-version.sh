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

if [[ -n "${CUSTOM_OM_VERSION:-}" ]]; then
  AGENT_VERSION="$(jq -r --arg om "${CUSTOM_OM_VERSION}" \
    '.supportedImages."mongodb-agent".opsManagerMapping.ops_manager[$om].agent_version // empty' \
    "${PROJECT_DIR}/release.json")"
  if [[ -z "${AGENT_VERSION}" ]]; then
    echo "resolve-agent-version.sh: no agent mapping for OM version ${CUSTOM_OM_VERSION} in release.json" >&2
    exit 1
  fi
else
  AGENT_VERSION="$(jq -r '.agentVersion' "${PROJECT_DIR}/release.json")"
fi

# Check for per-mode custom agent override
CUSTOM_URL=""
if [[ "${ops_manager_version:-}" == "cloud_qa" ]]; then
  CUSTOM_URL="$(jq -r '.customAgent.cloudqa // empty' "${PROJECT_DIR}/release.json")"
elif [[ -n "${CUSTOM_OM_VERSION:-}" ]]; then
  OM_MAJOR="${CUSTOM_OM_VERSION%%.*}"
  CUSTOM_URL="$(jq -r --arg m "${OM_MAJOR}" '.customAgent[$m] // empty' "${PROJECT_DIR}/release.json")"
fi
if [[ -n "${CUSTOM_URL}" ]]; then
  _filename="${CUSTOM_URL##*/}"
  _filename="${_filename%.tar.gz}"
  _rest="${_filename#mongodb-mms-automation-agent-}"
  _extracted="${_rest%.*}"
  if [[ -n "${_extracted}" ]]; then
    AGENT_VERSION="${_extracted}"
    export MDB_CUSTOM_AGENT_URL="${CUSTOM_URL}"
  else
    echo "resolve-agent-version.sh: failed to extract version from custom URL: ${CUSTOM_URL}" >&2
    exit 1
  fi
fi

export AGENT_VERSION
export AGENT_IMAGE="${MDB_AGENT_IMAGE_REPOSITORY}:${AGENT_VERSION}"
export MDB_COMMUNITY_AGENT_IMAGE="${AGENT_IMAGE}"
