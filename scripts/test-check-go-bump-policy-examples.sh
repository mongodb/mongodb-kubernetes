#!/usr/bin/env bash
# Example scenarios for scripts/check-go-bump-policy.sh (no network; no real gh).
#
# POLICY_UPGRADE_WINDOW_DAYS defaults to 90. EOL for the repo minor is set via
# TEST_OVERRIDE_CURRENT_EOL_DATE (skips endoflife fetch).
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
# Fake gh: no open bump PRs.
if [[ "${1:-}" == "pr" && "${2:-}" == "list" ]]; then
  echo '[]'
  exit 0
fi
echo "stub gh: only pr list supported" >&2
exit 1
STUB
chmod +x "${STUB_DIR}/gh"

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

echo "Running check-go-bump-policy examples (stub gh, no go.dev/endoflife fetch)..."
echo

# EOL 2026-08-01: outside default upgrade window on 2026-04-15, inside on 2026-05-31.
expect_pause "1) Apr 2026 — 1.25 vs 1.26, defer (EOL outside upgrade window)" \
  TEST_OVERRIDE_TODAY=2026-04-15 \
  TEST_OVERRIDE_CURRENT_EOL_DATE=2026-08-01 \
  TEST_OVERRIDE_LATEST_GO=1.26.2 \
  TEST_OVERRIDE_CURRENT_GO=1.25.9

expect_bump "2) May 31 2026 — 1.25 vs 1.26, within upgrade window before EOL" \
  TEST_OVERRIDE_TODAY=2026-05-31 \
  TEST_OVERRIDE_CURRENT_EOL_DATE=2026-08-01 \
  TEST_OVERRIDE_LATEST_GO=1.26.2 \
  TEST_OVERRIDE_CURRENT_GO=1.25.9 \
  TEST_BUMP_DRY_RUN=1

# EOL 2027-02-01: outside upgrade window on 2026-09-20, inside on 2026-11-15.
expect_pause "3) Sep 2026 — 1.26 vs 1.27, defer" \
  TEST_OVERRIDE_TODAY=2026-09-20 \
  TEST_OVERRIDE_CURRENT_EOL_DATE=2027-02-01 \
  TEST_OVERRIDE_LATEST_GO=1.27.0 \
  TEST_OVERRIDE_CURRENT_GO=1.26.2

expect_bump "4) Nov 2026 — 1.26 vs 1.27, within bumping period" \
  TEST_OVERRIDE_TODAY=2026-11-15 \
  TEST_OVERRIDE_CURRENT_EOL_DATE=2027-02-01 \
  TEST_OVERRIDE_LATEST_GO=1.27.1 \
  TEST_OVERRIDE_CURRENT_GO=1.26.2 \
  TEST_BUMP_DRY_RUN=1

# Past EOL → bump if not latest.
expect_bump "5) Mar 2027 — 1.26 vs 1.28, past current minor EOL, so bump" \
  TEST_OVERRIDE_TODAY=2027-03-18 \
  TEST_OVERRIDE_CURRENT_EOL_DATE=2027-02-01 \
  TEST_OVERRIDE_LATEST_GO=1.28.0 \
  TEST_OVERRIDE_CURRENT_GO=1.26.5 \
  TEST_BUMP_DRY_RUN=1

echo
if [[ "${failures}" -eq 0 ]]; then
  echo "All strict examples passed (${failures} failures)."
  exit 0
fi
echo "${failures} strict example(s) failed."
exit 1
