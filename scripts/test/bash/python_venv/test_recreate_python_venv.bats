#!/usr/bin/env bats
# recreate_python_venv.sh removes an existing venv directory and builds a new one. When
# PROJECT_VENV_PATH names a directory that is not a venv it stops and keeps the content.
# `uv` is a stub on PATH: `uv venv <dir>` makes <dir>/bin/activate.

load ../lib/fake_repo

setup() {
  make_fake_repo scripts/dev/recreate_python_venv.sh
  printf 'export PYTHON_VERSION=3.13\n' >>"${FAKE_REPO}/.generated/context.export.env"
  : >"${FAKE_REPO}/requirements.txt"
  printf '#!/usr/bin/env bash\n[[ "$1" == "venv" ]] && mkdir -p "$2/bin" && : >"$2/bin/activate"\nexit 0\n' >"${STUB_BIN}/uv"
  chmod +x "${STUB_BIN}/uv"
}

run_recreate() {
  run --separate-stderr env -u PROJECT_VENV_PATH PATH="${STUB_BIN}:${PATH}" "$@" \
    bash -c 'cd "'"${FAKE_REPO}"'" && bash scripts/dev/recreate_python_venv.sh'
}

@test "default path: a half-created venv without bin/activate is removed and rebuilt" {
  mkdir -p "${FAKE_REPO}/venv/lib"
  : >"${FAKE_REPO}/venv/lib/leftover"

  run_recreate

  assert_succeeded
  [ ! -e "${FAKE_REPO}/venv/lib/leftover" ]
  [ -f "${FAKE_REPO}/venv/bin/activate" ]
}

@test "PROJECT_VENV_PATH naming a directory that is not a venv: exit 1 and nothing is deleted" {
  local other="${BATS_TEST_TMPDIR}/not-a-venv"
  mkdir -p "${other}"
  echo keep >"${other}/important.txt"

  run_recreate PROJECT_VENV_PATH="${other}"

  [ "$status" -eq 1 ]
  [[ "${stderr}" == *"Refusing to replace ${other}"* ]]
  [ "$(cat "${other}/important.txt")" = "keep" ]
}

@test "PROJECT_VENV_PATH naming an existing venv: rebuilt" {
  local shared="${BATS_TEST_TMPDIR}/shared-venv"
  mkdir -p "${shared}/bin"
  : >"${shared}/bin/activate"
  : >"${shared}/old"

  run_recreate PROJECT_VENV_PATH="${shared}"

  assert_succeeded
  [ ! -e "${shared}/old" ]
  [ -f "${shared}/bin/activate" ]
}

@test "PROJECT_VENV_PATH naming an empty directory: a venv is created in it" {
  local empty="${BATS_TEST_TMPDIR}/empty"
  mkdir -p "${empty}"

  run_recreate PROJECT_VENV_PATH="${empty}"

  assert_succeeded
  [ -f "${empty}/bin/activate" ]
}
