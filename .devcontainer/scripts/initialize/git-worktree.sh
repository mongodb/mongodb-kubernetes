#!/bin/bash

# If the devcontainer workspace is a git worktree, we need to mount the original .git directory as a volume so git operations work correctly inside the container.

set -euo pipefail

# Get the path to the .git directory - if it's absolute then we're in a worktree
git_dir="$(git rev-parse --git-common-dir)"

if [[ "${git_dir}" == /* ]]; then
    # Check if the volume already exists
    if ! yq eval ".services.devcontainer.volumes[] | select(. == \"${git_dir}:${git_dir}:cached\")" "${COMPOSE_OVERRIDE_FILE}" | grep -q .; then
        yq eval -i ".services.devcontainer.volumes += [\"${git_dir}:${git_dir}:cached\"]" "${COMPOSE_OVERRIDE_FILE}"
        echo "Added git worktree volume: ${git_dir}"
    fi
fi
