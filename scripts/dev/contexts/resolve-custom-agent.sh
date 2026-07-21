#!/usr/bin/env bash
# Override agent version/image for all variants when a custom agent is set.
# This fixes the gap where OM variants (variables/om80) hardcode AGENT_VERSION,
# ignoring release.json.agentVersion.
#
# Precedence:
# 1. upstream_agent_url env var (cross-trigger from mms-automation)
# 2. customAgentUrl in release.json (manual)
# 3. empty (production, no override)

CUSTOM_AGENT_URL="${upstream_agent_url:-}"
if [[ -z "${CUSTOM_AGENT_URL}" ]]; then
  CUSTOM_AGENT_URL=$(jq -r '.customAgentUrl // empty' "${PROJECT_DIR}/release.json" 2>/dev/null || echo "")
fi

if [[ -n "${CUSTOM_AGENT_URL}" ]]; then
  if [[ -n "${upstream_agent_version:-}" ]]; then
    AGENT_VERSION="${upstream_agent_version}"
  else
    AGENT_VERSION=$(jq -r '.agentVersion' "${PROJECT_DIR}/release.json")
  fi
  AGENT_IMAGE="${MDB_AGENT_IMAGE_REPOSITORY}:${AGENT_VERSION}"
  MDB_COMMUNITY_AGENT_IMAGE="${AGENT_IMAGE}"
  export AGENT_VERSION AGENT_IMAGE MDB_COMMUNITY_AGENT_IMAGE
fi
