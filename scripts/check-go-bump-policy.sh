#!/usr/bin/env bash
# Go toolchain bump policy gate. When conditions pass, runs scripts/bump-go.sh
# (executor only; bump logic lives here).
#
# Invariant: bump only if the repo is not already on go.dev latest *and* the
# latest minor's .0 release is at least POLICY_SOAK_DAYS old (default 90).
# If the repo is 2+ minors behind latest (past Go's "N-1 supported" window),
# bump immediately regardless of soak. If there is no newer stable to adopt,
# never bump.
#
# Latest minor release date is derived from the GitHub tag go<latest_minor>.0
# (api.github.com), since Go does not publish EOL dates in a stable machine
# form and endoflife.date has been unreliable for Go.
#
# Tests: TEST_OVERRIDE_LATEST_GO, TEST_OVERRIDE_CURRENT_GO, TEST_OVERRIDE_TODAY,
#        TEST_OVERRIDE_LATEST_RELEASE_DATE (optional ISO; skips GitHub fetch)

set -euo pipefail

POLICY_SOAK_DAYS="${POLICY_SOAK_DAYS:-90}"

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
[[ -n "${TEST_OVERRIDE_LATEST_RELEASE_DATE:-}" ]] && _validate_iso "${TEST_OVERRIDE_LATEST_RELEASE_DATE}" TEST_OVERRIDE_LATEST_RELEASE_DATE

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
GO_MOD="${ROOT_DIR}/go.mod"
BUMP_SCRIPT="${ROOT_DIR}/scripts/bump-go.sh"
PR_SCRIPT="${ROOT_DIR}/scripts/create-go-bump-pr.sh"

[[ -f "${BUMP_SCRIPT}" ]] || {
  echo "check-go-bump-policy: error: missing ${BUMP_SCRIPT}" >&2
  exit 1
}
[[ -f "${PR_SCRIPT}" ]] || {
  echo "check-go-bump-policy: error: missing ${PR_SCRIPT}" >&2
  exit 1
}
[[ -f "${GO_MOD}" ]] || {
  echo "check-go-bump-policy: error: missing ${GO_MOD}" >&2
  exit 1
}

log_active_test_overrides() {
  local p=()
  [[ -n "${TEST_OVERRIDE_TODAY:-}" ]] && p+=("TEST_OVERRIDE_TODAY=${TEST_OVERRIDE_TODAY}")
  [[ -n "${TEST_OVERRIDE_LATEST_RELEASE_DATE:-}" ]] && p+=("TEST_OVERRIDE_LATEST_RELEASE_DATE=${TEST_OVERRIDE_LATEST_RELEASE_DATE}")
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

# Prints YYYY-MM-DD for the .0 release of the given "1.N" minor (or test override).
# GET commits/{ref} accepts tag names and resolves annotated tags transparently.
latest_minor_release_iso() {
  local tag="go$1.0" date
  if [[ -n "${TEST_OVERRIDE_LATEST_RELEASE_DATE:-}" ]]; then
    echo "${TEST_OVERRIDE_LATEST_RELEASE_DATE}"
    return 0
  fi
  date=$(gh api "repos/golang/go/commits/${tag}" --jq '.commit.committer.date[0:10]') || {
    echo "check-go-bump-policy: error: failed to fetch ${tag} from github" >&2
    return 1
  }
  [[ -n "${date}" ]] || {
    echo "check-go-bump-policy: error: empty date for ${tag}" >&2
    return 1
  }
  echo "${date}"
}

# 0 = defer, 1 = continue toward bump, 2 = error.
soak_gate() {
  local current_minor latest_minor
  current_minor="$(go_minor_label "$1")"
  latest_minor="$(go_minor_label "$2")"
  # Assumes both are 1.N (Go 2.x does not exist).
  local gap=$(( ${latest_minor#1.} - ${current_minor#1.} ))

  if [[ "${gap}" -ge 2 ]]; then
    echo "check-go-bump-policy: ${current_minor} is ${gap} minors behind ${latest_minor} (past Go N-1 support window) — bump$(test_clock_note)" >&2
    return 1
  fi

  local release_iso release_e td days_until
  release_iso="$(latest_minor_release_iso "${latest_minor}")" || return 2
  release_e=$(date_utc_epoch "${release_iso}") || return 2
  td=$(effective_today_epoch) || return 2
  days_until=$(( (release_e + POLICY_SOAK_DAYS * 86400 - td) / 86400 ))

  if [[ "${days_until}" -gt 0 ]]; then
    echo "check-go-bump-policy: defer bump: Go ${latest_minor} released ${release_iso}, ${days_until}d until ${POLICY_SOAK_DAYS}d soak elapses — skip$(test_clock_note)" >&2
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
  # Anchor on the branch name created by scripts/create-go-bump-pr.sh
  # (auto/bump-go-<version>) — PR titles can be edited/prefixed by reviewers,
  # branch names set by the automation cannot.
  local raw
  raw=$(gh pr list --state open --limit 100 --json number,title,url,headRefName) || {
    echo "check-go-bump-policy: error: gh pr list" >&2
    return 2
  }
  echo "${raw}" | jq -r '.[] | select(.headRefName | startswith("auto/bump-go-")) | "\(.number)\t\(.title)\t\(.url)"' | head -1
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

command -v gh >/dev/null 2>&1 || {
  echo "check-go-bump-policy: error: gh is required" >&2
  exit 1
}

latest="$(get_latest_published_go_version)" || exit 1
current="$(get_repository_go_version)" || exit 1

if [[ "${current}" != "${latest}" ]]; then
  _gate_rc=0
  soak_gate "${current}" "${latest}" || _gate_rc=$?
  case "${_gate_rc}" in
    0) exit 0 ;; # defer — within POLICY_SOAK_DAYS of latest minor release
    1) ;;        # past soak or gap>=2 — continue
    *) exit 1 ;; # lookup error
  esac
fi

pr="$(find_open_go_bump_pull_request)" || exit 1

if evaluate_go_bump_policy "${current}" "${latest}" "${pr}"; then
  _rc=0
else
  _rc=$?
fi

case "${_rc}" in
  0)
    "${BUMP_SCRIPT}" "${latest}"
    "${PR_SCRIPT}" "${latest}"
    ;;
10) exit 0 ;;
  *) exit 1 ;;
esac
