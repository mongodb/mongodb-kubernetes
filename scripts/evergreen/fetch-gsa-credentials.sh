#!/usr/bin/env bash

# Writes the EA license credentials into the om-license-gsa Secret of the test namespace.
# The values go through a file descriptor, so they never reach a command line, a file or a log.
#
# Usage: fetch-gsa-credentials.sh <secret-arn> <aws-profile>

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes
set +x

secret_arn="${1:?usage: fetch-gsa-credentials.sh <secret-arn> <aws-profile>}"
profile="${2:?usage: fetch-gsa-credentials.sh <secret-arn> <aws-profile>}"
: "${NAMESPACE:?NAMESPACE must be set, it comes from the dev context}"

# The secret lives in the region of its ARN, which is not the region of the rest of the pipeline.
region="$(cut -d: -f4 <<<"${secret_arn}")"

secret_string="$(aws secretsmanager get-secret-value \
  --profile "${profile}" \
  --region "${region}" \
  --secret-id "${secret_arn}" \
  --query SecretString \
  --output text)"
client_id="$(jq -re '.CLIENT_ID' <<<"${secret_string}")"
client_secret="$(jq -re '.CLIENT_SECRET' <<<"${secret_string}")"

ensure_namespace "${NAMESPACE}"
# The label keeps the credentials out of the diagnostics dump, so it is set in the same request that
# writes the Secret. create is used instead of apply, which would keep a plain text copy of the data
# in the last-applied-configuration annotation.
kubectl --namespace "${NAMESPACE}" delete secret om-license-gsa --ignore-not-found
kubectl --namespace "${NAMESPACE}" create secret generic om-license-gsa \
  --from-env-file=<(printf 'GSA_CLIENT_ID=%s\nGSA_CLIENT_SECRET=%s\n' "${client_id}" "${client_secret}") \
  --dry-run=client -o yaml \
  | kubectl label --local -f - mongodb.com/do-not-dump=true -o yaml \
  | kubectl --namespace "${NAMESPACE}" create -f -

echo "Wrote the EA license credentials to the om-license-gsa Secret in ${NAMESPACE}"
