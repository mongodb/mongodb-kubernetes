#!/bin/bash
# Copyright 2026 MongoDB Inc
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Returns the full kind node image ref for the min or max Kubernetes version.
# Usage: ./scripts/get-kind-image.sh [min|max]
# Example output: ghcr.io/mongodb/kindest-node:v1.36.1

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
VERSION_FILE="${VERSION_FILE:-${REPO_ROOT}/kubernetes-versions.json}"

if [ $# -ne 1 ]; then
    echo "Usage: $0 [min|max]" >&2
    exit 1
fi

VERSION_TYPE="${1}"

if [ "${VERSION_TYPE}" != "min" ] && [ "${VERSION_TYPE}" != "max" ]; then
    echo "Error: version type must be 'min' or 'max'" >&2
    exit 1
fi

if [ ! -f "${VERSION_FILE}" ]; then
    echo "Error: version file not found: ${VERSION_FILE}" >&2
    exit 1
fi

VERSION=$(jq -r ".kubernetes.${VERSION_TYPE}" "${VERSION_FILE}")
IMAGE=$(jq -r ".kind.images.${VERSION_TYPE}" "${VERSION_FILE}")

if [ -z "${VERSION}" ] || [ "${VERSION}" == "null" ]; then
    echo "Error: could not read kubernetes.${VERSION_TYPE} from ${VERSION_FILE}" >&2
    exit 1
fi

if [ -z "${IMAGE}" ] || [ "${IMAGE}" == "null" ]; then
    echo "Error: could not read kind.images.${VERSION_TYPE} from ${VERSION_FILE}" >&2
    exit 1
fi

echo "${IMAGE}:v${VERSION}"
