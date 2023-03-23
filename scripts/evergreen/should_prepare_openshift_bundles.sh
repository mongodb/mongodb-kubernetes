#!/usr/bin/env bash

# This file is a condition script used for conditionally executing evergreen task for generating openshift bundles (prepare_and_upload_openshift_bundles).

set -Eeou pipefail

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
community_bundle="https://operator-e2e-bundles.s3.amazonaws.com/bundles/operator-community-${version}.tgz"

if ! check_file_exists "${certified_bundle}"; then
  echo "Certified bundle file does not exist in S3: ${certified_bundle}"
  exit 0
fi

if ! check_file_exists "${community_bundle}"; then
  echo "Community bundle file does not exist in S3: ${community_bundle}"
  exit 0
fi

echo "Both certified and community bundles exists in S3. There is no need to generate openshift bundles.
  certified_bundle: ${certified_bundle}
  community_bundle: ${community_bundle}"

exit 1
