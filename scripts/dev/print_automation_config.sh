#!/usr/bin/env bash

set -Eeou pipefail -o posix
source scripts/dev/set_env_context.sh

# shellcheck disable=SC1090
source ~/.operator-dev/om
# shellcheck disable=SC1090
source ~/.operator-dev/context

file_name="$(mktemp).json"
group_id=$(curl -u "${OM_USER}:${OM_API_KEY}" "${OM_HOST}/api/public/v1.0/groups/byName/${NAMESPACE}" --digest -sS | jq -r .id)
curl -u "${OM_USER}:${OM_API_KEY}" "${OM_HOST}/api/public/v1.0/groups/${group_id}/automationConfig" --digest -sS | jq 'del(.mongoDbVersions)' > "${file_name}"
${EDITOR:-vi} "${file_name}"
