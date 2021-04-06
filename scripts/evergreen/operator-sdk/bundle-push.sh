#!/usr/bin/env bash
set -Eeou pipefail

VERSION="$(git describe --tags --abbrev=0)"
BUNDLE_IMG="scan.connect.redhat.com/ospid-52d1c6df-b3f6-432b-9646-adb7f689e581/operator-bundle:${VERSION}"


make bundle-annotated "VERSION=${VERSION}" IMG="registry.connect.redhat.com/mongodb/enterprise-operator:${VERSION}"
make bundle-build EXPIRES= "VERSION=${VERSION}" "BUNDLE_IMG=${BUNDLE_IMG}"
make docker-push "VERSION=${VERSION}" "IMG=${BUNDLE_IMG}"
