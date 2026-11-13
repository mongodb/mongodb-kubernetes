#!/usr/bin/env bash

# Prints the branch of the checked out PR head commit, or nothing if there is none (e.g. a fork).
# Uses ls-remote because the Evergreen source-cache clone has no refs/remotes/origin/<branch> for the PR head.

set -Eeou pipefail

git ls-remote --heads origin | awk -v sha="$(git rev-parse HEAD)" '$1 == sha && !seen++ { sub("^refs/heads/", "", $2); print $2 }'
