#!/usr/bin/env bash
set -Eeou pipefail

tag="$(git describe --tags)"
bundle_img="quay.io/mongodb/operator-bundle:${tag}"

operator-sdk run bundle "${bundle_img}" --timeout 4m0s --install-mode OwnNamespace
