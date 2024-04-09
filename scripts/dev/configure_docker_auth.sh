#!/usr/bin/env bash

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/checks
source scripts/funcs/printing
source scripts/funcs/kubernetes

remove_element() {
  config_option="${1}"
  tmpfile=$(mktemp)
  jq 'del(.'"${config_option}"')' ~/.docker/config.json >"${tmpfile}"
  cp "${tmpfile}" ~/.docker/config.json
  rm "${tmpfile}"
}

# This is the script which performs docker authentication to different registries that we use (so far ECR and RedHat)
# As the result of this login the ~/.docker/config.json will have all the 'auth' information necessary to work with docker registries

if [[ "${RUNNING_IN_EVG:-""}" == "true" ]]; then
  # when running locally we don't need to docker login all the time - we can do it once in 11 hours (ECR tokens expire each 12 hours)
  if [[ -n "$(find ~/.docker/config.json -mmin -360 -type f)" ]] &&
    grep "268558157000" -q ~/.docker/config.json; then
    echo "Docker credentials are up to date - not performing the new login!"
    exit
  fi
fi

title "Performing docker login to ECR registries"

# There could be some leftovers on Evergreen
if grep -q "credsStore" ~/.docker/config.json; then
  remove_element "credsStore"
fi
if grep -q "credHelpers" ~/.docker/config.json; then
  remove_element "credHelpers"
fi

echo "$(aws --version)}"

aws ecr get-login-password --region "us-east-1" | docker login --username AWS --password-stdin 268558157000.dkr.ecr.us-east-1.amazonaws.com

# by default docker tries to store credentials in an external storage (e.g. OS keychain) - not in the config.json
# We need to store it as base64 string in config.json instead so we need to remove the "credsStore" element
if grep -q "credsStore" ~/.docker/config.json; then
  remove_element "credsStore"

  # login again to store the credentials into the config.json
  aws ecr get-login-password --region "us-east-1" | docker login --username AWS --password-stdin 268558157000.dkr.ecr.us-east-1.amazonaws.com
fi
aws ecr get-login-password --region "eu-west-1" | docker login --username AWS --password-stdin 268558157000.dkr.ecr.eu-west-1.amazonaws.com


create_image_registries_secret
