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
ARTIFACTORY_URL=${ARTIFACTORY_URL}
SIGNING_IMAGE_URI=${SIGNING_IMAGE_URI}
ARTIFACTORY_PASSWORD=${ARTIFACTORY_PASSWORD}
ARTIFACTORY_USERNAME=${ARTIFACTORY_USERNAME}

echo "Signing artifact ${ARTIFACT} and saving signature bundle to ${SIGNATURE_BUNDLE}"

{
  echo "GRS_CONFIG_USER1_USERNAME=${GRS_USERNAME}";
  echo "GRS_CONFIG_USER1_PASSWORD=${GRS_PASSWORD}";
  echo "PKCS11_URI=${PKCS11_URI}";
} > "${SIGNING_ENVFILE}"

echo "Logging in artifactory.corp"
echo "${ARTIFACTORY_PASSWORD}" | docker login --password-stdin --username "${ARTIFACTORY_USERNAME}" "${ARTIFACTORY_URL}"

echo "Signing artifact"
echo "Envfile is ${SIGNING_ENVFILE}"
docker run \
  --env-file="${SIGNING_ENVFILE}" \
  --rm \
  -v "$(pwd)":"$(pwd)" \
  -w "$(pwd)" \
  "${SIGNING_IMAGE_URI}" \
  cosign sign-blob --key "${PKCS11_URI}" --bundle "${SIGNATURE_BUNDLE}" "${ARTIFACT}" --yes
