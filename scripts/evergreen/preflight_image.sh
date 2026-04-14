#!/usr/bin/env bash
#
# Resolves the image tag the same way scripts/release/pipeline.sh does (same env vars and case branches).
#
set -Eeou pipefail

cd "$(dirname "$0")/../.."

# shellcheck disable=SC1091
source scripts/dev/set_env_context.sh

PREFLIGHT_IMAGE="${PREFLIGHT_IMAGE:?}"
PREFLIGHT_SUBMIT="${PREFLIGHT_SUBMIT:?}"

# Mirror pipeline.sh's case ${IMAGE_NAME} in ... / IMAGE_VERSION=... (image names differ: mongodb-kubernetes vs operator).
case "${PREFLIGHT_IMAGE}" in
  mongodb-kubernetes | init-database | init-ops-manager | database)
    IMAGE_VERSION="${OPERATOR_VERSION}"
    ;;
  *)
    echo "unsupported preflight image: ${PREFLIGHT_IMAGE}" >&2
    exit 1
    ;;
esac

if [[ -z "${IMAGE_VERSION:-}" ]]; then
  echo "preflight_image.sh: empty version for ${PREFLIGHT_IMAGE}; pipeline.sh would omit --version — set the same env as for pipeline (e.g. OPERATOR_VERSION, OM_VERSION, AGENT_VERSION)." >&2
  exit 1
fi

exec scripts/dev/run_python.sh scripts/preflight_images.py \
  --image "${PREFLIGHT_IMAGE}" \
  --version "${IMAGE_VERSION}" \
  --submit "${PREFLIGHT_SUBMIT}"
