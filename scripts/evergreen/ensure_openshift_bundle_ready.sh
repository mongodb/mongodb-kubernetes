#!/usr/bin/env bash
set -Eeuo pipefail

version=$(jq -er .mongodbOperator < release.json)
bundle_file="mck-operator-certified-${version}.tgz"
bundle_url="https://operator-e2e-bundles.s3.amazonaws.com/bundles/${bundle_file}"

echo "Checking whether certified bundle is ready: ${bundle_url}"
set +e
http_status=$("${CURL_BIN:-curl}" --head --output /dev/null --write-out '%{http_code}' --silent --show-error "${bundle_url}")
curl_status=$?
set -e
if [[ "${curl_status}" -ne 0 ]]; then
  echo "Unable to check certified bundle at ${bundle_url} (curl exit ${curl_status})" >&2
  exit "${curl_status}"
fi

case "${http_status}" in
  200)
    echo "Certified bundle already exists; skipping preparation and upload."
    exit 0
    ;;
  404)
    echo "Certified bundle is missing; preparing and uploading it."
    ;;
  *)
    echo "Unexpected HTTP status while checking certified bundle: ${http_status}" >&2
    exit 1
    ;;
esac

export VERSION="${version}"
"${PREPARE_BUNDLE_SCRIPT:-scripts/evergreen/operator-sdk/prepare-openshift-bundles.sh}"

export AWS_ACCESS_KEY_ID="${enterprise_aws_access_key_id:?enterprise AWS access key is required}"
export AWS_SECRET_ACCESS_KEY="${enterprise_aws_secret_access_key:?enterprise AWS secret key is required}"
"${AWS_BIN:-aws}" s3 cp "bundle/${bundle_file}" "s3://operator-e2e-bundles/bundles/${bundle_file}" \
  --acl public-read \
  --content-type application/x-binary
