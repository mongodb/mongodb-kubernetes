#!/usr/bin/env bash
set -Eeou pipefail

# shellcheck disable=SC1091
source scripts/dev/set_env_context.sh

echo "Regenerating multi-cluster RBAC samples"
cd pkg/kubectl-mongodb/common
EXPORT_RBAC_SAMPLES=true go test ./... -run TestPrintingOutRolesServiceAccountsAndRoleBindings
git add ../../../public/samples/multi-cluster-cli-gitops
