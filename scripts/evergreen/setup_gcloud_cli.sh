#!/usr/bin/env bash
set -Eeou pipefail -o posix

source scripts/dev/set_env_context.sh

curl -s --retry 3 -LO "https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-cli-linux-x86_64.tar.gz"
tar xvf google-cloud-cli-linux-x86_64.tar.gz -C "${workdir}"
"${workdir}"/google-cloud-sdk/install.sh --quiet
source "${workdir}/google-cloud-sdk/path.bash.inc"

gcloud components install gke-gcloud-auth-plugin
echo "${GCP_SERVICE_ACCOUNT_JSON_FOR_SNIPPETS_TESTS}" > gcp_keyfile.json
gcloud auth activate-service-account --key-file gcp_keyfile.json
gcloud auth list
