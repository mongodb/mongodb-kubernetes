#!/usr/bin/env bats
# update_go_version.sh rewrites the files that name the Go version. It must keep their
# permissions, because pre-commit runs it on every commit and git records the executable bit.
# FULL_VERSION is set, so the script does not need Go.

setup() {
  local repo_root
  repo_root="$(cd "${BATS_TEST_DIRNAME}/../../../.." && pwd)"
  FAKE_ROOT="${BATS_TEST_TMPDIR}/repo"
  mkdir -p "${FAKE_ROOT}/scripts/dev/contexts"
  cp "${repo_root}/scripts/dev/update_go_version.sh" "${FAKE_ROOT}/scripts/dev/"
  printf 'export GOROOT=/opt/golang/go1.20\n' >"${FAKE_ROOT}/scripts/dev/prepare_local_e2e_run.sh"
  printf 'export GOROOT=/opt/golang/go1.20\n' >"${FAKE_ROOT}/scripts/dev/contexts/root-context"
  chmod 755 "${FAKE_ROOT}/scripts/dev/prepare_local_e2e_run.sh"
  chmod 644 "${FAKE_ROOT}/scripts/dev/contexts/root-context"
}

run_update() {
  run env FULL_VERSION=1.99.1 bash "${FAKE_ROOT}/scripts/dev/update_go_version.sh"
  [ "$status" -eq 0 ]
}

@test "update_go_version.sh writes the new Go version into the dev scripts" {
  run_update

  grep -qx 'export GOROOT=/opt/golang/go1.99' "${FAKE_ROOT}/scripts/dev/prepare_local_e2e_run.sh"
  grep -qx 'export GOROOT=/opt/golang/go1.99' "${FAKE_ROOT}/scripts/dev/contexts/root-context"
}

@test "update_go_version.sh keeps an executable script executable" {
  run_update

  [ -x "${FAKE_ROOT}/scripts/dev/prepare_local_e2e_run.sh" ]
}

@test "update_go_version.sh does not make a plain file executable" {
  run_update

  [ ! -x "${FAKE_ROOT}/scripts/dev/contexts/root-context" ]
}
