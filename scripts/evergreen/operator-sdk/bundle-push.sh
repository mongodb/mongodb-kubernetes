#!/usr/bin/env bash
set -Eeou pipefail

mkdir -p "${workdir:?}/bin"
operator_sdk_bin="${workdir:?}/bin/operator-sdk"
version="v1.12.0"
curl -L "https://github.com/operator-framework/operator-sdk/releases/download/${version}/operator-sdk_linux_amd64" -o "${operator_sdk_bin}"
chmod +x "${operator_sdk_bin}"

echo "Installed operator-sdk ${version} in ${operator_sdk_bin}"

VERSION="$(git describe --tags --abbrev=0)"
BUNDLE_IMG="scan.connect.redhat.com/ospid-52d1c6df-b3f6-432b-9646-adb7f689e581/operator-bundle:${VERSION}"


make bundle-annotated "VERSION=${VERSION}" IMG="registry.connect.redhat.com/mongodb/enterprise-operator:${VERSION}"
cp "./config/rbac/appdb_role.yaml" "./bundle/${VERSION}/manifests/mongodb-enterprise-appdb_rbac.authorization.k8s.io_v1_role.yaml"
cp "./config/rbac/appdb_role_binding.yaml" "./bundle/${VERSION}/manifests/mongodb-enterprise-appdb_rbac.authorization.k8s.io_v1_rolebinding.yaml"
make bundle-build EXPIRES= "VERSION=${VERSION}" "BUNDLE_IMG=${BUNDLE_IMG}"
make docker-push "VERSION=${VERSION}" "IMG=${BUNDLE_IMG}"


echo "Bundle has been pushed to Openshift Marketplace"
echo "You can review publishing state in https://connect.redhat.com/projects/5f7dcbbd9208171f730fbd03/images"
