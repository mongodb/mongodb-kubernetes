#!/usr/bin/env bash
# Stops agent-triggered child patches from silently testing the release agent.
# Assumes mck-agent-integration is the only trigger alias targeting MCK, revisit if another is added.
set -Eeou pipefail

if [[ -n "${PARENT_PATCH_ID:-}" && -z "${MDB_CUSTOM_AGENT_URL:-}" ]]; then
  echo "ERROR: this patch was created by parent patch ${PARENT_PATCH_ID}, but mdb_custom_agent_url is empty."
  echo "The parent agent build produced no tarball, or its branch does not pass mdb_custom_agent_url yet."
  echo "Refusing to run against the release agent. Fix the parent agent build and retrigger."
  exit 1
fi
