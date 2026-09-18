#!/usr/bin/env bash
#
# Post-run diagnostics shared by the CI e2e.sh and the on-host devcontainer
# e2e_gather_diagnostics.sh: dump per-cluster diagnostics and render the HTML
# test summary. Callers must source scripts/evergreen/e2e/dump_diagnostic_information.sh
# (for dump_all) before calling these.

# Dump diagnostics for every cluster in scope. Multi reads CENTRAL_CLUSTER +
# MEMBER_CLUSTERS; single uses the current kube context.
dump_cluster_information() {
  # TODO: ensure cluster name is included in log files so there is no overwriting of cross cluster files.
  # shellcheck disable=SC2154
  if [[ "${KUBE_ENVIRONMENT_NAME:-}" = "multi" ]]; then
    echo "Dumping diagnostics for context ${CENTRAL_CLUSTER}"
    dump_all "${CENTRAL_CLUSTER}" || true

    for member_cluster in ${MEMBER_CLUSTERS}; do
      echo "Dumping diagnostics for context ${member_cluster}"
      dump_all "${member_cluster}" || true
    done
  else
    dump_all "$(kubectl config current-context)" || true
  fi
}

# Render the HTML test summary. Fails silently so it never breaks the pipeline.
# The '!' output prefix sorts it to the top of Evergreen's file list.
# Usage: generate_test_summary <PASSED|FAILED> <test_name> <variant>
generate_test_summary() {
  local test_status="$1" test_name="$2" variant="$3"
  echo "Generating test summary..."
  if python3 scripts/evergreen/e2e/test_summary/generate_test_summary.py \
    logs \
    --output "logs/!test-summary.html" \
    --test-name "${test_name}" \
    --variant "${variant}" \
    --status "${test_status}" 2>&1 | tee logs/summary_generation.log; then
    echo "Test summary generated: logs/!test-summary.html"
  else
    echo "Warning: Failed to generate test summary (continuing anyway)"
  fi
}
