#!/usr/bin/env bash
#
# Smoke test: ``wt-ctl status`` from inside this worktree must print
# the banner + the keys ``worktree``, ``path``, ``network``, ``gost-proxy``.
#
set -Eeuo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/../.." && pwd)"
wt_ctl="${repo_root}/scripts/dev/wt-ctl"

stdout="$(mktemp)"
stderr="$(mktemp)"
trap 'rm -f "${stdout}" "${stderr}"' EXIT

if ! "${wt_ctl}" --color=never status 1>"${stdout}" 2>"${stderr}"; then
  echo "[smoke] wt-ctl status exited non-zero" >&2
  cat "${stderr}" >&2
  exit 1
fi

# Banner is on stderr.
if ! grep -q "^\[wt-ctl\] worktree=" "${stderr}"; then
  echo "[smoke] missing banner on stderr" >&2
  cat "${stderr}" >&2
  exit 1
fi

failures=0
for key in worktree path network gost-proxy; do
  if ! grep -q "^${key}\b" "${stdout}"; then
    echo "[smoke] missing key '${key}' in stdout" >&2
    failures=$((failures + 1))
  fi
done

if (( failures > 0 )); then
  echo "[smoke] FAIL (${failures} missing keys)" >&2
  echo "--- stdout ---" >&2
  cat "${stdout}" >&2
  echo "--- stderr ---" >&2
  cat "${stderr}" >&2
  exit 1
fi

echo "[smoke] ok"
