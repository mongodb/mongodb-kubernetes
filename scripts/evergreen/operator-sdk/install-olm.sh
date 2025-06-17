#!/usr/bin/env bash

set -Eeou pipefail -o posix

source scripts/dev/set_env_context.sh

operator-sdk olm install --version="${OLM_VERSION}"
