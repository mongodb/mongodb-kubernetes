#!/bin/bash

set -Eeou pipefail
source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes

operator-sdk olm install --version="${OLM_VERSION}" || true
make aws_login

if [[ "${EVG_HOST_NAME}" != "" ]]; then
  # to send ~/.docker/config.json to EVG host
  scripts/dev/evg_host.sh configure
fi

DELETE_CRD=true make reset
ensure_namespace "${NAMESPACE}"
scripts/dev/configure_operator.sh
