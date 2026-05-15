#!/usr/bin/env bash
#
# extract_member_kubeconfigs.sh — Phase D D'2.
#
# Split the multi-context kubeconfig used by the devc into N per-member-cluster
# kubeconfigs, one file per member cluster. Each output file contains exactly
# one context + one cluster + one user; nothing else. The current-context of
# each output is the member cluster.
#
# Default member contexts (matching the 4-cluster kind setup of this
# worktree): kind-e2e-cluster-1, kind-e2e-cluster-2, kind-e2e-cluster-3.
# Override via the MEMBER_CONTEXTS env var (space-separated). The operator
# context (kind-e2e-operator) is intentionally excluded — Phase D's
# distributed mode runs one operator process per member cluster, with no
# operator in the central/hub cluster.
#
# Output paths default to .generated/cluster-1.kubeconfig, cluster-2,
# cluster-3 (i.e. the trailing "kind-e2e-" prefix is dropped). The CLUSTER_NAME
# embedded in each file equals the bare cluster index ("cluster-1") so it
# matches the operator's CLUSTER_NAME env var configured by
# run-3-operators-locally.sh (D'4).
#
# Idempotent: rewrites the output files on every invocation.
#
# Usage:
#   scripts/dev/extract_member_kubeconfigs.sh                # default 3
#   MEMBER_CONTEXTS="kind-e2e-cluster-1 kind-e2e-cluster-2" \
#     scripts/dev/extract_member_kubeconfigs.sh               # subset
#   SRC_KUBECONFIG=/path/to/merged.kubeconfig \
#     scripts/dev/extract_member_kubeconfigs.sh

set -Eeuo pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"

SRC_KUBECONFIG="${SRC_KUBECONFIG:-.generated/current.devc.kubeconfig}"
OUT_DIR="${OUT_DIR:-.generated}"
MEMBER_CONTEXTS="${MEMBER_CONTEXTS:-kind-e2e-cluster-1 kind-e2e-cluster-2 kind-e2e-cluster-3}"

if [[ ! -f "${SRC_KUBECONFIG}" ]]; then
  echo "ERROR: source kubeconfig not found: ${SRC_KUBECONFIG}" >&2
  echo "       Run 'wt-ctl up' or refresh the kubeconfig and retry." >&2
  exit 1
fi

mkdir -p "${OUT_DIR}"

extract_one() {
  local ctx="$1"
  # Bare cluster identifier (drops the "kind-e2e-" kind prefix). Used as the
  # output filename stem and as the CLUSTER_NAME the operator will report.
  local stem
  stem="${ctx#kind-e2e-}"
  local out="${OUT_DIR}/${stem}.kubeconfig"

  echo "[extract] ${ctx} → ${out}"

  # `kubectl config view --minify --flatten` keeps only the resources
  # referenced by the current context. We set the current context first via
  # --context, then minify. --flatten inlines the certs so the result is a
  # standalone file (no external file references).
  KUBECONFIG="${SRC_KUBECONFIG}" \
    kubectl config view \
      --context="${ctx}" \
      --minify \
      --flatten \
      --raw \
      > "${out}.tmp"

  # Validate the output before moving it into place. If the source was a
  # device-context-only file (no master URL), the validation would fail.
  KUBECONFIG="${out}.tmp" kubectl --context="${ctx}" cluster-info >/dev/null 2>&1 || {
    echo "ERROR: extracted kubeconfig for ${ctx} failed cluster-info validation" >&2
    rm -f "${out}.tmp"
    exit 1
  }

  mv "${out}.tmp" "${out}"
  echo "[extract] ${out} validated (cluster-info OK)"
}

for ctx in ${MEMBER_CONTEXTS}; do
  extract_one "${ctx}"
done

echo
echo "[extract] Done. Produced files:"
# shellcheck disable=SC2086
ls -la $(for ctx in ${MEMBER_CONTEXTS}; do
  stem="${ctx#kind-e2e-}"
  echo "${OUT_DIR}/${stem}.kubeconfig"
done)
