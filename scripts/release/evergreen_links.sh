#!/usr/bin/env bash
# Writes the "Evergreen Links" step summary after the release tag is pushed,
# plus the manual Evergreen patch command as stdout.
#
# Called from .github/workflows/release.yml (step "Show Evergreen links"),
# which exports VERSION and COMMIT_SHA.
#
# Locally testable: when GITHUB_STEP_SUMMARY is unset, summary lines print to
# stdout instead, e.g.:
#   VERSION=1.2.3 COMMIT_SHA=<sha> scripts/release/evergreen_links.sh
set -Eeou pipefail

VERSION="${VERSION:?VERSION must be set}"
COMMIT_SHA="${COMMIT_SHA:?COMMIT_SHA must be set}"

summary() {
    if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
        echo "$1" >> "${GITHUB_STEP_SUMMARY}"
    else
        echo "$1"
    fi
}

summary "## 🔗 Evergreen Links"
summary ""
summary "The tag has been pushed. Check Evergreen for the triggered build:"
summary ""
summary "- **[Tag-triggered builds (waterfall)](https://spruce.corp.mongodb.com/project/mongodb-kubernetes/waterfall?requesters=git_tag_request)**"
summary "- **[All project patches](https://spruce.corp.mongodb.com/project/mongodb-kubernetes/patches)**"
summary ""
summary "Once the Evergreen publish pipeline completes successfully, approve the next job to continue."

# stdout only (intended for the job log, not the step summary).
echo "In case you need to kick the patch manually:"
echo "  evergreen patch -p mongodb-kubernetes -v release_decider_patch \\"
echo "    -t decide_release_process -d \"Release version ${VERSION}\" \\"
echo "    -f -y -u --path .evergreen.yml \\"
echo "    --param BUILD_SCENARIO=release --param triggered_by_git_tag=${VERSION} \\"
echo "    --param target_commit=${COMMIT_SHA}"
