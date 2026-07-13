#!/usr/bin/env bats
#
# Regression tests for CI scripts that must diff against the current branch's
# own remote state instead of a hardcoded origin/master. Builds a throwaway
# local git remote with master + v1 branches so the origin/v1 comparison can
# be exercised without a real GitHub push or Evergreen project.
#
# Run via: scripts/test/bash/run.sh backport_branch

setup() {
    PROJECT_DIR="$(git rev-parse --show-toplevel)"
    SCRIPT="${PROJECT_DIR}/scripts/evergreen/should_release_agents_on_ecr.sh"
    RBAC_SCRIPT="${PROJECT_DIR}/scripts/dev/regenerate_multicluster_rbac.sh"

    WORK_DIR="$(mktemp -d)"
    REMOTE_DIR="${WORK_DIR}/remote.git"
    CLONE_DIR="${WORK_DIR}/clone"

    git init --bare -q "${REMOTE_DIR}"
    git clone -q "${REMOTE_DIR}" "${CLONE_DIR}"

    cd "${CLONE_DIR}"
    git config user.email "test@example.com"
    git config user.name "test"

    echo '{"marker": "master-value"}' > release.json
    git add release.json
    git commit -q -m "master release.json"
    git branch -m master
    git push -q origin master

    git checkout -q -b v1
    echo '{"marker": "v1-value"}' > release.json
    git add release.json
    git commit -q -m "v1 release.json diverges from master"
    git push -q origin v1
}

teardown() {
    rm -rf "${WORK_DIR}"
}

@test "should_release_agents_on_ecr: skips when release.json unchanged vs its own branch" {
    cd "${CLONE_DIR}"
    git checkout -q v1

    run env branch_name=v1 "${SCRIPT}"

    [ "$status" -eq 1 ]
    [[ "$output" == *"has not changed"* ]]
}

@test "should_release_agents_on_ecr: does NOT false-trigger just because v1 differs from master" {
    cd "${CLONE_DIR}"
    git checkout -q v1

    # release.json on v1 differs from master's, but nothing has changed
    # *within* v1 itself — comparing against origin/master (the old bug)
    # would incorrectly report a change here.
    run env branch_name=v1 "${SCRIPT}"

    [ "$status" -eq 1 ]
}

@test "should_release_agents_on_ecr: triggers when release.json changes on v1" {
    cd "${CLONE_DIR}"
    git checkout -q v1
    echo '{"marker": "v1-value-modified"}' > release.json

    run env branch_name=v1 "${SCRIPT}"

    [ "$status" -eq 0 ]
    [[ "$output" == *"has changed"* ]]
}

@test "regenerate_multicluster_rbac: lists v1's own tree, not master's" {
    cd "${CLONE_DIR}"
    git checkout -q v1
    mkdir -p pkg/kubectl-mongodb
    echo "marker" > pkg/kubectl-mongodb/only_on_v1.go
    git add pkg/kubectl-mongodb
    git commit -q -m "add kubectl-mongodb path only on v1"
    git push -q origin v1

    tree_listing=$(env branch_name=v1 git ls-tree -r "origin/v1" --name-only)

    [[ "${tree_listing}" == *"pkg/kubectl-mongodb/only_on_v1.go"* ]]

    master_tree_listing=$(git ls-tree -r "origin/master" --name-only)
    [[ "${master_tree_listing}" != *"pkg/kubectl-mongodb/only_on_v1.go"* ]]
}
