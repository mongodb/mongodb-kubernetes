#!/usr/bin/env bash

set -Eeou pipefail

# script prepares environment variables relevant for the current context
# TODO add the context overriding via parameter

source scripts/funcs/errors

mkdir -p ~/.operator-dev

context="$1"
context_file="${HOME}/.operator-dev/contexts/${context}"

if [[ ! -f "${context_file}" ]]; then
	fatal "Cannot switch context: File ${context_file} does not exist."
fi

rm "${HOME}/.operator-dev/context"
ln -s "${HOME}/.operator-dev/contexts/${context}" "${HOME}/.operator-dev/context"

# Reading environment variables for the context - this is where we get "CLUSTER_NAME" var from
source scripts/dev/read_context.sh

echo "Switched operator context to ${context}"

