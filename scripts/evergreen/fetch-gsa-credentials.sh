#!/usr/bin/env bash

# Writes the EA license credentials to the om-license-gsa Secret, never to a command line, file or log.
# Usage: fetch-gsa-credentials.sh <secret-arn> <aws-profile>

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/kubernetes
set +x

secret_arn="${1:?usage: fetch-gsa-credentials.sh <secret-arn> <aws-profile>}"
profile="${2:?usage: fetch-gsa-credentials.sh <secret-arn> <aws-profile>}"
: "${NAMESPACE:?NAMESPACE must be set, it comes from the dev context}"

# The secret region comes from its ARN.
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
# apply would keep a plain text copy in the last-applied-configuration annotation.
kubectl --namespace "${NAMESPACE}" delete secret om-license-gsa --ignore-not-found
kubectl --namespace "${NAMESPACE}" create secret generic om-license-gsa \
  --from-env-file=<(printf 'GSA_CLIENT_ID=%s\nGSA_CLIENT_SECRET=%s\n' "${client_id}" "${client_secret}")

echo "Wrote the EA license credentials to the om-license-gsa Secret in ${NAMESPACE}"
