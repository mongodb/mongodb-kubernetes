#!/usr/bin/env bash
#
# Gather Kubernetes diagnostics + the HTML test summary into logs/ using the
# same dump_cluster_information + generate_test_summary from
# scripts/evergreen/e2e/diagnostics.sh that CI e2e.sh runs. Runs inside the
# devcontainer after e2e_run.sh so the on-host --local-kind flow produces the
# same artifact set under logs/ that upload_e2e_logs archives to S3.
#
# Usage: e2e_gather_diagnostics.sh <test_rc> [test_name] [variant]
#

root="$(git rev-parse --show-toplevel 2>/dev/null || echo /workspace)"
cd "${root}" || exit 1

test_rc="${1:-0}"
test_name="${2:-${TASK_NAME:-unknown}}"
variant="${3:-unknown}"

# shellcheck disable=SC1091
source scripts/dev/set_env_context.sh
source scripts/funcs/printing
source scripts/evergreen/e2e/dump_diagnostic_information.sh
source scripts/evergreen/e2e/diagnostics.sh

# A single failed kubectl must never abort diagnostics; -u would trip on
# MEMBER_CLUSTERS in the single-cluster branch.
set +eu

mkdir -p logs

dump_cluster_information

test_status="FAILED"
[[ "${test_rc}" -eq 0 ]] && test_status="PASSED"
generate_test_summary "${test_status}" "${test_name}" "${variant}" || true

echo "Diagnostics gathered into ${root}/logs (status=${test_status})"
