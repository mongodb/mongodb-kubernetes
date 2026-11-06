#!/usr/bin/env bash
#
# A script Evergreen will use to setup openshift-preflight
set -Eeou pipefail
source scripts/dev/set_env_context.sh
source scripts/funcs/install

bindir="${PROJECT_DIR:?}/bin"
mkdir -p "${bindir}"

echo "Downloading preflight binary"
# 1.22.0 carries the upstream fix for truncated layer streams during image
# extraction. Preflight pulls and verifies every layer before extraction, and
# retries each layer with backoff:
#   https://github.com/redhat-openshift-ecosystem/openshift-preflight/pull/1507
#   https://github.com/redhat-openshift-ecosystem/openshift-preflight/pull/1549
#
# Releases before 1.21.0 had no retry in the extraction path. A transient
# registry drop during release preflight failed the run with
#   failed to extract tarball: unexpected EOF
# We worked around it with a 5-attempt retry around the whole command
# (CLOUDP-298165):
#   https://github.com/mongodb/mongodb-kubernetes/commit/6f2a5fa39ebb8f58e069292ed8c30c33436bba97
# That loop stays as a second layer of protection. It reruns the full pull,
# extraction, and check pipeline on each attempt.
#
# Pyxis accepts only the preflight versions in its supported window at submit
# time, so this pin must track upstream releases.
preflight_version="1.22.0"
curl_with_retry -s -o preflight -L "https://github.com/redhat-openshift-ecosystem/openshift-preflight/releases/download/${preflight_version}/preflight-linux-amd64"
chmod +x preflight
mv preflight "${bindir}"
echo "Installed preflight to ${bindir}"
