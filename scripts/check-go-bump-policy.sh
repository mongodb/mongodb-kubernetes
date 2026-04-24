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

# Epoch → YYYY-MM-DD UTC.
epoch_to_utc_iso() {
  local e="$1" s
  if s=$(date -u -d "@${e}" +%Y-%m-%d 2>/dev/null); then echo "${s}"; return 0; fi
  if s=$(date -u -r "${e}" +%Y-%m-%d 2>/dev/null); then echo "${s}"; return 0; fi
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

# Numeric minor gap (latest - current). Assumes both share the same major.
go_minor_gap() {
  local current="$1" latest="$2"
  local ca cb la lb _
  IFS=. read -r ca cb _ <<<"${current}"
  IFS=. read -r la lb _ <<<"${latest}"
  if [[ "${ca}" != "${la}" ]]; then
    echo "check-go-bump-policy: error: major mismatch ${ca} vs ${la}" >&2
    return 1
  fi
  echo $((lb - cb))
}

effective_today_epoch() {
  if [[ -n "${TEST_OVERRIDE_TODAY:-}" ]]; then
    date_utc_epoch "${TEST_OVERRIDE_TODAY}"
  else
    date_utc_epoch "$(date -u +%Y-%m-%d)"
  fi
}

# GitHub API fetch honoring GH_TOKEN / GITHUB_TOKEN if present.
_gh_api() {
  local url="$1"
  local auth=()
  local tok="${GH_TOKEN:-${GITHUB_TOKEN:-}}"
  [[ -n "${tok}" ]] && auth=(-H "Authorization: Bearer ${tok}")
  curl -fsSL --max-time 60 "${auth[@]}" \
    -H 'Accept: application/vnd.github+json' \
    -H 'X-GitHub-Api-Version: 2022-11-28' \
    "${url}"
}

# Prints YYYY-MM-DD for the .0 release of the given "1.N" minor (or test override).
latest_minor_release_iso() {
  local minor="$1"
  local tag="go${minor}.0"

  if [[ -n "${TEST_OVERRIDE_LATEST_RELEASE_DATE:-}" ]]; then
    echo "${TEST_OVERRIDE_LATEST_RELEASE_DATE}"
    return 0
  fi

  local ref_json sha obj_type obj_json date
  ref_json="$(_gh_api "https://api.github.com/repos/golang/go/git/refs/tags/${tag}")" || {
    echo "check-go-bump-policy: error: failed to resolve tag ${tag} on github" >&2
    return 1
  }
  sha="$(printf '%s' "${ref_json}" | jq -r '.object.sha // empty')"
  obj_type="$(printf '%s' "${ref_json}" | jq -r '.object.type // empty')"
  [[ -n "${sha}" && -n "${obj_type}" ]] || {
    echo "check-go-bump-policy: error: malformed ref payload for ${tag}" >&2
    return 1
  }

  if [[ "${obj_type}" == "tag" ]]; then
    obj_json="$(_gh_api "https://api.github.com/repos/golang/go/git/tags/${sha}")" || {
      echo "check-go-bump-policy: error: failed to fetch annotated tag ${tag}" >&2
      return 1
    }
    date="$(printf '%s' "${obj_json}" | jq -r '.tagger.date // empty')"
  else
    obj_json="$(_gh_api "https://api.github.com/repos/golang/go/git/commits/${sha}")" || {
      echo "check-go-bump-policy: error: failed to fetch commit ${sha} for ${tag}" >&2
      return 1
    }
    date="$(printf '%s' "${obj_json}" | jq -r '.committer.date // empty')"
  fi
  [[ -n "${date}" ]] || {
    echo "check-go-bump-policy: error: missing date on ${tag}" >&2
    return 1
  }
  echo "${date%%T*}"
}

# 0 = defer, 1 = continue toward bump, 2 = error.
soak_gate() {
  local current="$1" latest="$2"
  local current_minor latest_minor gap
  current_minor="$(go_minor_label "${current}")"
  latest_minor="$(go_minor_label "${latest}")"
  gap="$(go_minor_gap "${current_minor}" "${latest_minor}")" || return 2

  if [[ "${gap}" -ge 2 ]]; then
    echo "check-go-bump-policy: ${current_minor} is ${gap} minors behind ${latest_minor} (past Go N-1 support window) — bump$(test_clock_note)" >&2
    return 1
  fi

  local release_iso release_e eligible_e td days_until
  release_iso="$(latest_minor_release_iso "${latest_minor}")" || return 2
  release_e=$(date_utc_epoch "${release_iso}") || return 2
  td=$(effective_today_epoch) || return 2
  eligible_e=$((release_e + POLICY_SOAK_DAYS * 86400))
  days_until=$(((eligible_e - td) / 86400))

  if [[ "${td}" -lt "${eligible_e}" ]]; then
    local eligible_iso
    eligible_iso="$(epoch_to_utc_iso "${eligible_e}")" || return 2
    echo "check-go-bump-policy: defer bump: Go ${latest_minor} released ${release_iso}, bump_eligible_from ${eligible_iso} (${days_until}d, ${POLICY_SOAK_DAYS}d soak) — skip$(test_clock_note)" >&2
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
  0)
    "${BUMP_SCRIPT}" "${latest}"
    "${PR_SCRIPT}" "${latest}"
    ;;
10) exit 0 ;;
  *) exit 1 ;;
esac
