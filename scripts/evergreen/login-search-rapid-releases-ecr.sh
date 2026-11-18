#!/usr/bin/env bash

# Logs Docker into the search rapid-release ECR, after
# write-devprod-platforms-ecr-aws-profile.sh has written the assumed role's credentials.

set -Eeou pipefail

SEARCH_RAPID_RELEASES_REGISTRY="${SEARCH_RAPID_RELEASES_REGISTRY:-901841024863.dkr.ecr.us-east-1.amazonaws.com}"
SEARCH_RAPID_RELEASES_AWS_PROFILE="${SEARCH_RAPID_RELEASES_AWS_PROFILE:-devprod-platforms-ecr}"

aws --profile "${SEARCH_RAPID_RELEASES_AWS_PROFILE}" ecr get-login-password --region us-east-1 \
  | docker login --username AWS --password-stdin "${SEARCH_RAPID_RELEASES_REGISTRY}"
