#!/usr/bin/env bash
# Downloads the oc binary and logs into the OpenShift cluster.
# Requires OPENSHIFT_TOKEN and OPENSHIFT_URL to be set in the environment.
set -Eeou pipefail

source scripts/funcs/install

bindir="${PROJECT_DIR:-${workdir}}/bin"
OC_PKG=oc-linux.tar.gz

mkdir -p "${bindir}"
curl_with_retry -s -L 'https://operator-kubernetes-build.s3.amazonaws.com/oc-4.12.8-linux.tar.gz' --output "${OC_PKG}"
tar xfz "${OC_PKG}" &>/dev/null
mv oc "${bindir}"
rm -f "${OC_PKG}"

# https://stackoverflow.com/c/private-cloud-kubernetes/questions/15
oc login --token="${OPENSHIFT_TOKEN}" --server="${OPENSHIFT_URL}"
