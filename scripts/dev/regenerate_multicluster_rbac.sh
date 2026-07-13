#!/usr/bin/env bash
#
# Regenerate multicluster RBAC public example if relevant files changed.
#

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/printing

git_last_changed=$(git ls-tree -r "origin/${branch_name:-master}" --name-only)

if echo "${git_last_changed}" | grep -q -e 'cmd/kubectl-mongodb' -e 'pkg/kubectl-mongodb'; then
  echo 'regenerating multicluster RBAC public example'
  pushd pkg/kubectl-mongodb/common/
  EXPORT_RBAC_SAMPLES="true" go test ./... -run TestPrintingOutRolesServiceAccountsAndRoleBindings
  popd
  git add public/samples/multi-cluster-cli-gitops
fi
