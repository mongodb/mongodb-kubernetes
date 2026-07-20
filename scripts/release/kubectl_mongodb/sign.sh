#!/usr/bin/env bash
# Disables shellcheck on lines 15-21, because vars seem to be assigned to themselves.
# and we are not sure if removing this would be an issue.
# shellcheck disable=SC2269

set -euo pipefail

# Sign a binary using garasign credentials

ARTIFACT=$1
SIGNATURE_BUNDLE="${ARTIFACT}.bundle"

TMPDIR=${TMPDIR:-/tmp}
SIGNING_ENVFILE="${TMPDIR}/signing-envfile"

GRS_USERNAME=${GRS_USERNAME}
GRS_PASSWORD=${GRS_PASSWORD}
PKCS11_URI=${PKCS11_URI}
SIGNING_IMAGE_URI=${SIGNING_IMAGE_URI}

echo "Signing artifact ${ARTIFACT} and saving signature bundle to ${SIGNATURE_BUNDLE}"

{
  echo "GRS_CONFIG_USER1_USERNAME=${GRS_USERNAME}";
  echo "GRS_CONFIG_USER1_PASSWORD=${GRS_PASSWORD}";
  echo "PKCS11_URI=${PKCS11_URI}";
} > "${SIGNING_ENVFILE}"

# SIGNING_IMAGE_URI's registry is controlled by an Evergreen project expansion, so this
# transparently supports either registry depending on what that expansion is set to.
SIGNING_REGISTRY="${SIGNING_IMAGE_URI%%/*}"
if [[ "${SIGNING_REGISTRY}" == *.amazonaws.com ]]; then
  echo "Logging in to ${SIGNING_REGISTRY} (ECR)"
  aws ecr get-login-password --region us-east-1 --profile devprod-platforms-ecr | docker login --username AWS --password-stdin "${SIGNING_REGISTRY}"
else
  ARTIFACTORY_URL=${ARTIFACTORY_URL}
  ARTIFACTORY_PASSWORD=${ARTIFACTORY_PASSWORD}
  ARTIFACTORY_USERNAME=${ARTIFACTORY_USERNAME}
  echo "Logging in artifactory.corp"
  echo "${ARTIFACTORY_PASSWORD}" | docker login --password-stdin --username "${ARTIFACTORY_USERNAME}" "${ARTIFACTORY_URL}"
fi

echo "Signing artifact"
echo "Envfile is ${SIGNING_ENVFILE}"
docker run \
  --env-file="${SIGNING_ENVFILE}" \
  --rm \
  -v "$(pwd)":"$(pwd)" \
  -w "$(pwd)" \
  "${SIGNING_IMAGE_URI}" \
  cosign sign-blob --key "${PKCS11_URI}" --tlog-upload=false --use-signing-config=false --bundle "${SIGNATURE_BUNDLE}" "${ARTIFACT}" --yes
