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

ln -sf "${HOME}/.operator-dev/contexts/${context}" "${HOME}/.operator-dev/context"

# Reading environment variables for the context - this is where we get "CLUSTER_NAME" var from
source scripts/dev/read_context.sh
# print all environments variables set in read_context.sh to env file
# this will render all variable substitutions
(set -o posix; env -i HOME="${HOME}" PATH="${PATH}" bash -c "source scripts/dev/set_env_context.sh; printenv | grep -v '^PATH=' | sed 's/=\(.*\)/=\"\1\"/' >${context_file}.env")
scripts/dev/print_operator_env.sh >"${context_file}.operator.env"
awk '{print "export " $0}' < "${context_file}".env > "${context_file}".export.env
awk '{print "export " $0}' < "${context_file}".operator.env > "${context_file}".operator.export.env

echo "Generated env files: "
ls -l1 ~/.operator-dev/context*.env
