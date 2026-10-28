# Shared by the bats suites that run repo scripts in a scratch checkout: the real
# set_env_context.sh and scripts/funcs, plus the files a suite names, with commands replaced by
# stubs on PATH that log their argv to CALL_LOG.

bats_require_minimum_version 1.5.0

# make_fake_repo <path relative to the repo root>...
# Sets REPO_ROOT, FAKE_REPO, FAKE_HOME, STUB_BIN and CALL_LOG and copies the named files. The
# context has PROJECT_DIR only; a suite appends what its scripts read.
make_fake_repo() {
  REPO_ROOT="$(cd "${BATS_TEST_DIRNAME}/../../../.." && pwd)"
  FAKE_REPO="${BATS_TEST_TMPDIR}/repo"
  FAKE_HOME="${BATS_TEST_TMPDIR}/home"
  STUB_BIN="${BATS_TEST_TMPDIR}/bin"
  CALL_LOG="${BATS_TEST_TMPDIR}/calls.log"
  mkdir -p "${FAKE_REPO}/scripts/dev" "${FAKE_REPO}/scripts/funcs" "${FAKE_REPO}/.generated" \
    "${FAKE_HOME}" "${STUB_BIN}"
  : >"${CALL_LOG}"
  local file
  for file in scripts/dev/set_env_context.sh "$@"; do
    mkdir -p "${FAKE_REPO}/$(dirname "${file}")"
    cp "${REPO_ROOT}/${file}" "${FAKE_REPO}/${file}"
  done
  cp "${REPO_ROOT}"/scripts/funcs/* "${FAKE_REPO}/scripts/funcs/"
  printf 'export PROJECT_DIR=%s\n' "${FAKE_REPO}" >"${FAKE_REPO}/.generated/context.export.env"
}

# stub_logger <command>...: each command becomes a stub that logs "<command> <args>" and exits 0.
stub_logger() {
  local name
  for name in "$@"; do
    printf '#!/usr/bin/env bash\necho "%s $*" >>"${CALL_LOG}"\n' "${name}" >"${STUB_BIN}/${name}"
    chmod +x "${STUB_BIN}/${name}"
  done
}

# Fails the test and prints the script's output when it did not exit 0.
assert_succeeded() {
  if [ "$status" -ne 0 ]; then
    echo "exit status ${status}" >&2
    echo "${output}" >&2
    echo "${stderr:-}" >&2
    return 1
  fi
}

# Fails when the pattern (a basic regex) is in the file. A bare `! grep` never fails a bats test.
refute_in_file() {
  if grep -q -- "$1" "$2"; then
    echo "unexpected '$1' in $2:" >&2
    cat "$2" >&2
    return 1
  fi
}

calls_matching() {
  grep -c -- "$1" "${CALL_LOG}" || true
}
