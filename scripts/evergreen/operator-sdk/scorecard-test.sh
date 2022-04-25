#!/usr/bin/env bash
set -Eeou pipefail

tag="$(git describe --tags)"
bundle_img="quay.io/mongodb/operator-bundle:${tag}"

operator-sdk scorecard "${bundle_img}"
