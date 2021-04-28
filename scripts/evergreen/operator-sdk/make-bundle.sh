#!/usr/bin/env bash
set -Eeou pipefail

operator_sdk_bin="${workdir:?}/bin/operator-sdk"
version="v1.5.0"
curl -L "https://github.com/operator-framework/operator-sdk/releases/download/${version}/operator-sdk_linux_amd64" -o "${workdir}/bin/operator-sdk"
chmod +x "${operator_sdk_bin}"

echo "Installed operator-sdk ${version} in ${operator_sdk_bin}"

operator-sdk olm install --version v0.17.0

echo "Installed Operator Lifecycle Management (olm) into cluster."

# The operator-bundle image will go to a public quay repo, because operator-sdk
# can't be configured to use pivate registries yet.
echo "${quay_prod_robot_token:?}" | docker login quay.io/mongodb --password-stdin --username "${quay_prod_username:?}"

docker pull "${operator_img:?}"
kind load docker-image "${operator_img}"

tag="$(git describe)"
bundle_img="quay.io/mongodb/operator-bundle:${tag}"

make bundle-annotated VERSION="${tag}" IMG="${operator_img:?}"
make bundle-build VERSION="${tag}" BUNDLE_IMG="${bundle_img}"
make docker-push IMG="${bundle_img}"
