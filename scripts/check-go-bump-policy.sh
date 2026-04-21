#!/usr/bin/env bash
# Go toolchain bump policy gate. When conditions pass, runs scripts/bump-go.sh
# (executor only; bump logic lives here).
#
# Invariant: bump only if the repo is not already on go.dev latest *and* the current
# minor's EOL (endoflife.date) is within POLICY_UPGRADE_WINDOW_DAYS (default
# 90 days, ~3 months, tunable). If there is no newer stable to adopt, we never bump.
#
# https://endoflife.date/api/v1/products/go/
#
# Tests: TEST_OVERRIDE_LATEST_GO, TEST_OVERRIDE_CURRENT_GO, TEST_OVERRIDE_TODAY,
#        TEST_OVERRIDE_CURRENT_EOL_DATE (optional ISO; skips endoflife fetch for EOL)

set -euo pipefail

POLICY_UPGRADE_WINDOW_DAYS="${POLICY_UPGRADE_WINDOW_DAYS:-90}"

if [[ $# -gt 0 ]]; then
  echo "check-go-bump-policy: error: no arguments (see header)" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "check-go-bump-policy: error: jq is required" >&2
  exit 1
fi

# Date handling: GNU coreutils (Linux) vs BSD (macOS).

# YYYY-MM-DD → UTC midnight epoch.
date_utc_epoch() {
  local d="$1" s
  if s=$(date -u -d "${d} 00:00:00" +%s 2>/dev/null); then echo "${s}"; return 0; fi
  if s=$(date -u -j -f "%Y-%m-%d" "${d}" +%s 2>/dev/null); then echo "${s}"; return 0; fi
  return 1
}

_validate_iso() {
  date_utc_epoch "$1" >/dev/null 2>&1 || {
    echo "check-go-bump-policy: error: $2 must be YYYY-MM-DD" >&2
    exit 1
  }
}

[[ -n "${TEST_OVERRIDE_TODAY:-}" ]] && _validate_iso "${TEST_OVERRIDE_TODAY}" TEST_OVERRIDE_TODAY
[[ -n "${TEST_OVERRIDE_CURRENT_EOL_DATE:-}" ]] && _validate_iso "${TEST_OVERRIDE_CURRENT_EOL_DATE}" TEST_OVERRIDE_CURRENT_EOL_DATE

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
GO_MOD="${ROOT_DIR}/go.mod"
BUMP_SCRIPT="${ROOT_DIR}/scripts/bump-go.sh"

[[ -f "${BUMP_SCRIPT}" ]] || {
  echo "check-go-bump-policy: error: missing ${BUMP_SCRIPT}" >&2
  exit 1
}
[[ -f "${GO_MOD}" ]] || {
  echo "check-go-bump-policy: error: missing ${GO_MOD}" >&2
  exit 1
}

log_active_test_overrides() {
  local p=()
  [[ -n "${TEST_OVERRIDE_TODAY:-}" ]] && p+=("TEST_OVERRIDE_TODAY=${TEST_OVERRIDE_TODAY}")
  [[ -n "${TEST_OVERRIDE_CURRENT_EOL_DATE:-}" ]] && p+=("TEST_OVERRIDE_CURRENT_EOL_DATE=${TEST_OVERRIDE_CURRENT_EOL_DATE}")
  [[ -n "${TEST_OVERRIDE_LATEST_GO:-}" ]] && p+=("TEST_OVERRIDE_LATEST_GO=${TEST_OVERRIDE_LATEST_GO}")
  [[ -n "${TEST_OVERRIDE_CURRENT_GO:-}" ]] && p+=("TEST_OVERRIDE_CURRENT_GO=${TEST_OVERRIDE_CURRENT_GO}")
  if ((${#p[@]} > 0)); then
    echo "check-go-bump-policy: note: ${p[*]}" >&2
  fi
}

test_clock_note() {
  local b=()
  [[ -n "${TEST_OVERRIDE_TODAY:-}" ]] && b+=("TODAY=${TEST_OVERRIDE_TODAY}")
  if ((${#b[@]} > 0)); then
    printf ' (%s)' "${b[*]}"
  fi
}

strip_go_prefix() {
  local v="$1"
  [[ "${v}" == go* ]] && echo "${v#go}" || echo "${v}"
}

go_minor_label() {
  local a b _
  IFS=. read -r a b _ <<<"$1"
  echo "${a}.${b}"
}

effective_today_epoch() {
  if [[ -n "${TEST_OVERRIDE_TODAY:-}" ]]; then
    date_utc_epoch "${TEST_OVERRIDE_TODAY}"
  else
    date_utc_epoch "$(date -u +%Y-%m-%d)"
  fi
}

# Prints eolFrom YYYY-MM-DD for repo minor (or test override).
current_minor_eol_iso() {
  local json="$1" current_full="$2"
  local minor eol

  if [[ -n "${TEST_OVERRIDE_CURRENT_EOL_DATE:-}" ]]; then
    echo "${TEST_OVERRIDE_CURRENT_EOL_DATE}"
    return 0
  fi

  minor="$(go_minor_label "${current_full}")"
  eol=$(printf '%s' "${json}" | jq -r --arg m "${minor}" '.result.releases[] | select(.name == $m) | .eolFrom // empty' | head -1)
  [[ -n "${eol}" ]] || {
    echo "check-go-bump-policy: no eolFrom for Go ${minor} on endoflife.date" >&2
    return 1
  }
  echo "${eol}"
}

# 0 = defer, 1 = continue toward bump, 2 = error
upgrade_window_gate() {
  local current="$1" latest="$2" json="$3"
  local eol_iso eol_e td gate_sec sec_left days_left minor

  eol_iso="$(current_minor_eol_iso "${json}" "${current}")" || return 2
  eol_e=$(date_utc_epoch "${eol_iso}") || return 2
  td=$(effective_today_epoch) || return 2
  gate_sec=$((POLICY_UPGRADE_WINDOW_DAYS * 86400))
  sec_left=$((eol_e - td))
  days_left=$((sec_left / 86400))

  minor="$(go_minor_label "${current}")"
  if [[ "${sec_left}" -gt "${gate_sec}" ]]; then
    echo "check-go-bump-policy: defer bump: Go ${minor} EOL ${eol_iso} is ${days_left}d away (>${POLICY_UPGRADE_WINDOW_DAYS}d gate) — skip$(test_clock_note)" >&2
    return 0
  fi
  return 1
}

_json_latest_stable_go_raw() {
  curl -fsSL --max-time 60 'https://go.dev/dl/?mode=json' | jq -r '[.[] | select(.stable == true)][0].version'
}

get_repository_go_version() {
  local v
  if [[ -n "${TEST_OVERRIDE_CURRENT_GO:-}" ]]; then
    v="$(strip_go_prefix "${TEST_OVERRIDE_CURRENT_GO}")"
  else
    v="$(grep -E '^go[[:space:]]+[0-9]' "${GO_MOD}" | head -1 | awk '{print $2}' | tr -d '\r')"
  fi
  [[ -n "${v}" ]] || {
    echo "check-go-bump-policy: error: no go in go.mod" >&2
    return 1
  }
  echo "${v}"
}

get_latest_published_go_version() {
  local raw norm
  if [[ -n "${TEST_OVERRIDE_LATEST_GO:-}" ]]; then
    raw="${TEST_OVERRIDE_LATEST_GO}"
  else
    raw="$(_json_latest_stable_go_raw)" || {
      echo "check-go-bump-policy: error: go.dev fetch or parse failed" >&2
      return 1
    }
    [[ -n "${raw}" && "${raw}" != "null" ]] || {
      echo "check-go-bump-policy: error: go.dev fetch or parse failed" >&2
      return 1
    }
  fi
  norm="$(strip_go_prefix "${raw}")"
  [[ -n "${norm}" && "${norm}" != "null" ]] || {
    echo "check-go-bump-policy: error: bad latest" >&2
    return 1
  }
  echo "${norm}"
}

find_open_go_bump_pull_request() {
  local raw
  raw=$(gh pr list --state open --limit 100 --json number,title,url) || {
    echo "check-go-bump-policy: error: gh pr list" >&2
    return 2
  }
  echo "${raw}" | jq -r '.[] | select(.title | test("bump go|go bump|bump golang|upgrade go|go toolchain|go version"; "i")) | "\(.number)\t\(.title)\t\(.url)"' | head -1
}

evaluate_go_bump_policy() {
  local current="$1" latest="$2" pr_line="$3"
  [[ -n "${current}" && -n "${latest}" ]] || {
    echo "check-go-bump-policy: error: evaluate args" >&2
    return 1
  }
  [[ "${current}" == "${latest}" ]] && {
    echo "check-go-bump-policy: already at latest ${latest} — skip"
    return 10
  }
  local hi
  hi="$(printf '%s\n' "${current}" "${latest}" | sort -V | tail -1)"
  [[ "${current}" == "${hi}" && "${current}" != "${latest}" ]] && {
    echo "check-go-bump-policy: ahead of go.dev — skip"
    return 10
  }
  if [[ -n "${pr_line}" ]]; then
    local n t u
    IFS=$'\t' read -r n t u <<<"${pr_line}"
    echo "check-go-bump-policy: open bump PR #${n} — skip"
    echo "check-go-bump-policy:   ${t}"
    echo "check-go-bump-policy:   ${u}"
    return 10
  fi
  echo "check-go-bump-policy: enforce: ${current} < ${latest} — bump-go.sh ${latest}"
  return 0
}

# --- main
log_active_test_overrides

latest="$(get_latest_published_go_version)" || exit 1
current="$(get_repository_go_version)" || exit 1

_eol_json=""
if [[ "${current}" != "${latest}" ]]; then
  if [[ -z "${TEST_OVERRIDE_CURRENT_EOL_DATE:-}" ]]; then
    _eol_json="$(curl -fsSL --max-time 60 'https://endoflife.date/api/v1/products/go/')" || {
      echo "check-go-bump-policy: error: endoflife.date fetch failed" >&2
      exit 1
    }
  else
    _eol_json="{}"
  fi
  _gate_rc=0
  upgrade_window_gate "${current}" "${latest}" "${_eol_json}" || _gate_rc=$?
  case "${_gate_rc}" in
    0) exit 0 ;; # defer — outside POLICY_UPGRADE_WINDOW_DAYS of current minor EOL
    1) ;;        # within gate or past EOL — continue
    *) exit 1 ;; # EOL resolution error
  esac
fi

command -v gh >/dev/null 2>&1 || {
  echo "check-go-bump-policy: error: need gh" >&2
  exit 1
}
pr="$(find_open_go_bump_pull_request)" || exit 1

if evaluate_go_bump_policy "${current}" "${latest}" "${pr}"; then
  _rc=0
else
  _rc=$?
fi

case "${_rc}" in
  0) exec "${BUMP_SCRIPT}" "${latest}" ;;
10) exit 0 ;;
  *) exit 1 ;;
esac
