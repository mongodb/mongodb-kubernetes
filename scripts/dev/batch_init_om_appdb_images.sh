#!/usr/bin/env bash
set -Eeou pipefail


source scripts/dev/set_env_context.sh
source scripts/funcs/printing


header "Building Init Ops Manager Image"
scripts/dev/build_push_init_opsmanager_image.sh
