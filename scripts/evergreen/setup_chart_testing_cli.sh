#!/usr/bin/env bash

# A script Evergreen will use to setup helm chart testing CLI (https://github.com/helm/chart-testing)
#
# This should be executed from root of the evergreen build dir

set -Eeou pipefail

source scripts/dev/set_env_context.sh

bindir="${PROJECT_DIR}/bin"
tmpdir="${PROJECT_DIR}/tmp"
mkdir -p "${bindir}" "${tmpdir}"

curl -s --retry 3 -L -o "${tmpdir}/chart-testing_3.13.0_linux_amd64.tar.gz" "https://github.com/helm/chart-testing/releases/download/v3.13.0/chart-testing_3.13.0_linux_amd64.tar.gz"
tar xvf "${tmpdir}/chart-testing_3.13.0_linux_amd64.tar.gz" -C "${tmpdir}/"
mv "${tmpdir}/ct" "${bindir}"
