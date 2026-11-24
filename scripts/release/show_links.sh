#!/usr/bin/env bash
# Writes link summaries to stdout, for the workflow to tee into its job
# summary. Usage: show_links.sh {evergreen|redhat-prs}
#
# Called from .github/workflows/release.yml, which exports the needed env
# vars (VERSION, COMMIT_SHA, EVERGREEN_PROJECT for evergreen; VERSION, DRY_RUN
# for redhat-prs). EVERGREEN_PROJECT is required in evergreen mode.
#
# Locally testable, e.g.:
#   VERSION=1.2.3 COMMIT_SHA=<sha> EVERGREEN_PROJECT=mongodb-kubernetes scripts/release/show_links.sh evergreen
set -Eeou pipefail
MODE="${1:-}"
case "${MODE}" in
evergreen)
    VERSION="${VERSION:?VERSION must be set}"
    COMMIT_SHA="${COMMIT_SHA:?COMMIT_SHA must be set}"
    EVERGREEN_PROJECT="${EVERGREEN_PROJECT:?EVERGREEN_PROJECT must be set}"
    echo "## 🔗 Evergreen Links"
    echo ""
    echo "The tag has been pushed. Check Evergreen for the triggered build:"
    echo ""
    echo "- **[Tag-triggered builds (waterfall)](https://spruce.corp.mongodb.com/project/${EVERGREEN_PROJECT}/waterfall?requesters=git_tag_request)**"
    echo "- **[All project patches](https://spruce.corp.mongodb.com/project/${EVERGREEN_PROJECT}/patches)**"
    echo ""
    echo "Once the Evergreen publish pipeline completes successfully, approve the next job to continue."
    echo "In case you need to kick the patch manually:"
    echo "  evergreen patch -p ${EVERGREEN_PROJECT} -v release_decider_patch \\"
    echo "    -t decide_release_process -d \"Release version ${VERSION}\" \\"
    echo "    -f -y -u --path .evergreen.yml \\"
    echo "    --param BUILD_SCENARIO=release --param triggered_by_git_tag=${VERSION} \\"
    echo "    --param target_commit=${COMMIT_SHA}"
    ;;
redhat-prs)
    VERSION="${VERSION:?VERSION must be set}"
    DRY_RUN="${DRY_RUN:?DRY_RUN must be set}"
    RH_FORK_ORG="mongodb-forks"
    COMMUNITY_REPO="community-operators"
    CERTIFIED_REPO="certified-operators"
    RH_BRANCH="mongodb-kubernetes-${VERSION}"
    COMMUNITY_PR_CREATION_URL="https://github.com/k8s-operatorhub/community-operators/compare/main...${RH_FORK_ORG}:${COMMUNITY_REPO}:${RH_BRANCH}?quick_pull=1"
    CERTIFIED_PR_CREATION_URL="https://github.com/redhat-openshift-ecosystem/certified-operators/compare/main...${RH_FORK_ORG}:${CERTIFIED_REPO}:${RH_BRANCH}?quick_pull=1"
    if [[ "${DRY_RUN}" == "true" ]]; then
        echo "## 🚀 Dry Run: RedHat PR were not created, ignore the rest of the text..."
    fi
    echo "## 🚀 Evergreen Finished: RedHat PR Ready"
    echo "Evergreen successfully created the release branches on the RedHat fork."
    echo ""
    echo "👉 **[Click here to create the RedHat Community Operators PR](${COMMUNITY_PR_CREATION_URL})**"
    echo "👉 **[Click here to create the RedHat Certified Operators PR](${CERTIFIED_PR_CREATION_URL})**"
    ;;
*)
    echo "Usage: show_links.sh {evergreen|redhat-prs}" >&2
    exit 1
    ;;
esac
