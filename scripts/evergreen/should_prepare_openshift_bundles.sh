#!/usr/bin/env bash

# This file is a condition script used for conditionally executing evergreen task for generating openshift bundles (prepare_and_upload_openshift_bundles).

set -Eeou pipefail
source scripts/dev/set_env_context.sh

check_file_exists() {
  url=$1
  stderr_out=$(mktemp)
  echo "Checking if file exists: ${url}..."
  http_error_code=$(curl -o /dev/stderr --head --write-out '%{http_code}' --silent "${url}" 2>"${stderr_out}")
  echo "http error code=${http_error_code}"
  cat "${stderr_out}"
  rm "${stderr_out}"
  if [[ "${http_error_code}" == "200" ]]; then
    return 0
  else
    return 1
  fi
}

version=$(jq -r .mongodbOperator <release.json)
certified_bundle="https://operator-e2e-bundles.s3.amazonaws.com/bundles/operator-certified-${version}.tgz"

if ! check_file_exists "${certified_bundle}"; then
  echo "Certified bundle file does not exist in S3: ${certified_bundle}"
  exit 0
fi

echo "Сertified bundle exists in S3. There is no need to generate openshift bundle: ${certified_bundle}"

exit 1
