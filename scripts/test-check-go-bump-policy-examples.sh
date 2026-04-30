#!/usr/bin/env bash
# Example scenarios for scripts/check-go-bump-policy.sh (no network; no real gh).
#
# POLICY_SOAK_DAYS defaults to 90. The .0 release date of the latest minor is
# set via TEST_OVERRIDE_LATEST_RELEASE_DATE (skips the GitHub tag fetch).
#
# Usage: from repo root: ./scripts/test-check-go-bump-policy-examples.sh

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
POLICY="${ROOT}/scripts/check-go-bump-policy.sh"
STUB_DIR=
failures=0

# Invoked via trap EXIT (shellcheck does not trace trap handlers: SC2329/SC2317).
# shellcheck disable=SC2329,SC2317
cleanup() {
  [[ -n "${STUB_DIR}" && -d "${STUB_DIR}" ]] && rm -rf "${STUB_DIR}"
}
trap cleanup EXIT

if [[ ! -x "${POLICY}" ]]; then
  chmod +x "${POLICY}" "${ROOT}/scripts/bump-go.sh" 2>/dev/null || true
fi

STUB_DIR="$(mktemp -d)"
cat >"${STUB_DIR}/gh" <<'STUB'
#!/usr/bin/env bash
# Fake gh: returns $STUB_GH_PR_LIST_JSON for `pr list` (default: empty array).
if [[ "${1:-}" == "pr" && "${2:-}" == "list" ]]; then
  echo "${STUB_GH_PR_LIST_JSON:-[]}"
  exit 0
fi
echo "stub gh: only pr list supported" >&2
exit 1
STUB
chmod +x "${STUB_DIR}/gh"

expect_skip_pr() {
  local name="$1"
  shift
  local out rc
  echo "── ${name}"
  set +e
  out="$(PATH="${STUB_DIR}:${PATH}" env "$@" "${POLICY}" 2>&1)"
  rc=$?
  set -e
  if [[ "${rc}" -ne 0 ]]; then
    echo "FAIL: exit ${rc}, expected 0 (skip)"
    echo "${out}"
    failures=$((failures + 1))
    return
  fi
  if ! grep -q 'open bump PR' <<<"${out}"; then
    echo "FAIL: expected open-bump-PR skip message"
    echo "${out}"
    failures=$((failures + 1))
    return
  fi
  echo "PASS"
}

expect_pause() {
  local name="$1"
  shift
  local out rc
  echo "── ${name}"
  set +e
  out="$(PATH="${STUB_DIR}:${PATH}" env "$@" "${POLICY}" 2>&1)"
  rc=$?
  set -e
  if [[ "${rc}" -ne 0 ]]; then
    echo "FAIL: exit ${rc}, expected 0 (pause)"
    echo "${out}"
    failures=$((failures + 1))
    return
  fi
  if ! grep -q 'defer bump:' <<<"${out}"; then
    echo "FAIL: expected defer bump message"
    echo "${out}"
    failures=$((failures + 1))
    return
  fi
  echo "PASS"
}

expect_bump() {
  local name="$1"
  shift
  local out rc
  echo "── ${name}"
  set +e
  out="$(PATH="${STUB_DIR}:${PATH}" env "$@" "${POLICY}" 2>&1)"
  rc=$?
  set -e
  if [[ "${rc}" -ne 0 ]]; then
    echo "FAIL: exit ${rc}, expected 0 (bump path)"
    echo "${out}"
    failures=$((failures + 1))
    return
  fi
  if ! grep -qE 'bump-go: bumping Go version to' <<<"${out}"; then
    echo "FAIL: expected bump-go.sh output"
    echo "${out}"
    failures=$((failures + 1))
    return
  fi
  echo "PASS"
}

echo "Running check-go-bump-policy examples (stub gh, no go.dev/github fetch)..."
echo

# Latest minor (1.26) released 2026-02-10. Soak ends 2026-05-11.
expect_pause "1) Mar 2026 — 1.25 vs 1.26, within 90d soak, defer" \
  TEST_OVERRIDE_TODAY=2026-03-15 \
  TEST_OVERRIDE_LATEST_RELEASE_DATE=2026-02-10 \
  TEST_OVERRIDE_LATEST_GO=1.26.0 \
  TEST_OVERRIDE_CURRENT_GO=1.25.9

expect_bump "2) May 31 2026 — 1.25 vs 1.26, past 90d soak, bump" \
  TEST_OVERRIDE_TODAY=2026-05-31 \
  TEST_OVERRIDE_LATEST_RELEASE_DATE=2026-02-10 \
  TEST_OVERRIDE_LATEST_GO=1.26.2 \
  TEST_OVERRIDE_CURRENT_GO=1.25.9 \
  TEST_BUMP_DRY_RUN=1

# Latest minor (1.27) released 2026-08-10. Soak ends 2026-11-08.
expect_pause "3) Sep 2026 — 1.26 vs 1.27, within 90d soak, defer" \
  TEST_OVERRIDE_TODAY=2026-09-20 \
  TEST_OVERRIDE_LATEST_RELEASE_DATE=2026-08-10 \
  TEST_OVERRIDE_LATEST_GO=1.27.0 \
  TEST_OVERRIDE_CURRENT_GO=1.26.2

expect_bump "4) Nov 2026 — 1.26 vs 1.27, past 90d soak, bump" \
  TEST_OVERRIDE_TODAY=2026-11-15 \
  TEST_OVERRIDE_LATEST_RELEASE_DATE=2026-08-10 \
  TEST_OVERRIDE_LATEST_GO=1.27.1 \
  TEST_OVERRIDE_CURRENT_GO=1.26.2 \
  TEST_BUMP_DRY_RUN=1

# gap >= 2 short-circuits soak regardless of release date / today.
expect_bump "5) Mar 2027 — 1.26 vs 1.28, 2 minors behind, bump immediately" \
  TEST_OVERRIDE_TODAY=2027-03-18 \
  TEST_OVERRIDE_LATEST_RELEASE_DATE=2027-02-01 \
  TEST_OVERRIDE_LATEST_GO=1.28.0 \
  TEST_OVERRIDE_CURRENT_GO=1.26.5 \
  TEST_BUMP_DRY_RUN=1

# Even within soak window, a 2-minor gap forces a bump (support-lost guardrail).
expect_bump "6) gap>=2 overrides soak — latest just released, current 2 behind" \
  TEST_OVERRIDE_TODAY=2027-02-05 \
  TEST_OVERRIDE_LATEST_RELEASE_DATE=2027-02-01 \
  TEST_OVERRIDE_LATEST_GO=1.28.0 \
  TEST_OVERRIDE_CURRENT_GO=1.26.5 \
  TEST_BUMP_DRY_RUN=1

# PR-dedup: an open PR on the auto/bump-go-* branch should skip.
expect_skip_pr "7) open auto/bump-go-* PR — skip" \
  TEST_OVERRIDE_TODAY=2026-05-31 \
  TEST_OVERRIDE_LATEST_RELEASE_DATE=2026-02-10 \
  TEST_OVERRIDE_LATEST_GO=1.26.2 \
  TEST_OVERRIDE_CURRENT_GO=1.25.9 \
  STUB_GH_PR_LIST_JSON='[{"number":42,"title":"anything reviewers typed","url":"https://example/42","headRefName":"auto/bump-go-1.26.2"}]'

# PR-dedup: an unrelated PR whose title merely mentions "go" must NOT suppress bumps.
expect_bump "8) unrelated PR with 'go' in title — still bump" \
  TEST_OVERRIDE_TODAY=2026-05-31 \
  TEST_OVERRIDE_LATEST_RELEASE_DATE=2026-02-10 \
  TEST_OVERRIDE_LATEST_GO=1.26.2 \
  TEST_OVERRIDE_CURRENT_GO=1.25.9 \
  TEST_BUMP_DRY_RUN=1 \
  STUB_GH_PR_LIST_JSON='[{"number":7,"title":"refactor: go routines in controller","url":"https://example/7","headRefName":"feature/controller-goroutines"}]'

echo
if [[ "${failures}" -eq 0 ]]; then
  echo "All strict examples passed (${failures} failures)."
  exit 0
fi
echo "${failures} strict example(s) failed."
exit 1
