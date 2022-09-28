#!/usr/bin/env bash
set -Eeou pipefail

operator_sdk_bin="${workdir:?}/bin/operator-sdk"
version="v1.23.0"
curl -L "https://github.com/operator-framework/operator-sdk/releases/download/${version}/operator-sdk_linux_amd64" -o "${workdir}/bin/operator-sdk"
chmod +x "${operator_sdk_bin}"

echo "Installed operator-sdk ${version} in ${operator_sdk_bin}"

tag="${triggered_by_git_tag:-$(git describe --tags)}"

make bundle-annotated VERSION="${tag}" IMG="quay.io/mongodb/mongodb-enterprise-operator:${tag}"
