#!/usr/bin/env bash
set -Eeou pipefail

cd "$(git rev-parse --show-toplevel || echo "Failed to find git root"; exit 1)"

source scripts/dev/set_env_context
source scripts/funcs/printing


# FIXME: remove when we switch to static appdb builds
header "Building AppDB Image"
scripts/dev/DEPRECATED_BUILD_PUSH_APPDB_IMAGE.sh


header "Building Init Ops Manager Image"
scripts/dev/build_push_init_opsmanager_image.sh
header "Building Init AppDB Image"
scripts/dev/build_push_init_appdb_image.sh
