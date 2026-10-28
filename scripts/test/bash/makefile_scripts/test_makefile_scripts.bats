#!/usr/bin/env bats
# Makefile recipes that start with a script path run it directly, so the script must be
# executable in git.

@test "every script a Makefile recipe runs directly is executable" {
  local repo_root="${BATS_TEST_DIRNAME}/../../../.."
  local scripts
  scripts="$(grep -oE '^	@?(\./)?scripts/[A-Za-z0-9_/.-]+\.sh' "${repo_root}/Makefile" | sed -E 's#^	@?(\./)?##' | sort -u)"
  [ -n "${scripts}" ]

  local script not_executable=""
  for script in ${scripts}; do
    if [[ "$(git -C "${repo_root}" ls-files -s -- "${script}" | cut -c1-6)" != "100755" ]]; then
      not_executable+=" ${script}"
    fi
  done

  [ -z "${not_executable}" ] || { echo "not executable in git:${not_executable}" >&2; return 1; }
}
