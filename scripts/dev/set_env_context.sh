#!/usr/bin/env bash

set -Eeou pipefail

# script prepares environment variables relevant for the current context
# If it's run locally ($IN_MEMORY_CONTEXT is not defined) then the context variables
# are read from ~/.operator_dev/context


# shellcheck disable=1091
source scripts/funcs/errors

if [[ -z "${IN_MEMORY_CONTEXT-}" ]]; then
    # Reading context file
    readonly root_dir="$HOME/.operator-dev"
    readonly context_file="$root_dir/context"

    if [[ -f "${root_dir}/current" ]] && [[ ! -f ${context_file} ]]; then
        # Transform old 'current' file into sumbolic link
        # shellcheck disable=SC2086
        ln -s "${root_dir}/contexts/$(<"${root_dir}/current")" "${context_file}"
    fi

    if [[ ! -f ${context_file} ]]; then
        fatal "File ${context_file} not found! You must init development environment using 'make init' first."
    fi

    # reading the 'om' file first and then the context file - this will allow to use custom connectivity parameters
    if [[ -f ${root_dir}/om ]]; then
        # shellcheck disable=SC1090
        source "${root_dir}/om"
    fi

    # shellcheck disable=SC1090
    source "${context_file}"

    # LOCAL_RUN indicates that the make script is run locally. This may affect different build/deploy decisions
    export LOCAL_RUN=true
    # version_id is similar to version_id from Evergreen. Used to differentiate different builds. Can be constant
    # for local run
    export version_id="latest"
else
    echo "Skipping reading context file."
    echo "Note that all the configuration information \
        (REPO_URL, CLUSTER_TYPE) must be provided as environment variables!"

    # LOCAL_RUN=false indicates that the make script is run by Evergreen. This may affect different build/deploy decisions
    export LOCAL_RUN=false
fi

# guessing type of registry by url
# regular expression matching (https://www.tldp.org/LDP/abs/html/string-manipulation.html)
if [[ $(expr "${BASE_REPO_URL}" : '^localhost.*') -gt 0 ]]; then
    export REPO_TYPE="local"
elif [[ $(expr "${BASE_REPO_URL}" : '.*\.ecr\..*') -gt 0 ]]; then
    export REPO_TYPE="ecr"
else
    fatal "Failed to guess repository type based on url \"${REPO_URL}\""
fi

# IMAGE_TYPE is mandatory
if [[ "${IMAGE_TYPE}" != "ubuntu" ]] && [[ "${IMAGE_TYPE}" != "ubi" ]]; then
    fatal "'IMAGE_TYPE' env var must one of 'ubuntu' or 'ubi'"
fi

# Appending image type (ubuntu/ubi) to the registry url (unless it's already appended)
export REPO_URL=${BASE_REPO_URL}/${IMAGE_TYPE}

# By default all "raw" (meaning there are no startup scripts or extra binaries) images are read from
# quay.io as they are not rebuilt during building process
[[ -z "${OPS_MANAGER_REGISTRY-}" ]] && export OPS_MANAGER_REGISTRY="quay.io/mongodb"
[[ -z "${APPDB_REGISTRY-}" ]] && export APPDB_REGISTRY="quay.io/mongodb"
[[ -z "${DATABASE_REGISTRY-}" ]] && export DATABASE_REGISTRY="quay.io/mongodb"

[[ -z "${INIT_DATABASE_REGISTRY-}" ]] && export INIT_DATABASE_REGISTRY="${REPO_URL}"
[[ -z "${INIT_APPDB_REGISTRY-}" ]] && export INIT_APPDB_REGISTRY="${REPO_URL}"
[[ -z "${INIT_OPS_MANAGER_REGISTRY-}" ]] && export INIT_OPS_MANAGER_REGISTRY="${REPO_URL}"

# Test app as it's the only image not dependent on image type
[[ -z "${TEST_APP_REGISTRY-}" ]] && export TEST_APP_REGISTRY="${BASE_REPO_URL}"

export NAMESPACE=${NAMESPACE:-mongodb}

if [[ -z "${IN_MEMORY_CONTEXT-}" ]]; then
    OPERATOR_DIR="${root_dir}"
    CURRENT_CONTEXT="$(readlink "${context_file}")"

    export OPERATOR_DIR
    export CURRENT_CONTEXT
fi
