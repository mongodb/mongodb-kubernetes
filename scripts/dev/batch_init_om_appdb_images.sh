#!/usr/bin/env bash
set -Eeou pipefail

cd "$(git rev-parse --show-toplevel)"

source scripts/dev/set_env_context.sh
source scripts/funcs/printing


header "Building Init Ops Manager Image"
scripts/dev/build_push_init_opsmanager_image.sh
header "Building Init AppDB Image"
scripts/dev/build_push_init_appdb_image.sh
