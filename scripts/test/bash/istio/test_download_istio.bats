#!/usr/bin/env bats
# download_istio.sh verifies the checksum of the Istio download script with the tool the host
# has: shasum on macOS, sha256sum elsewhere. curl, uname and the two checksum tools are stubs on
# PATH that log their argv; curl "downloads" a script that logs when it runs.

load ../lib/fake_repo

setup() {
  make_fake_repo multi_cluster/tools/download_istio.sh
  stub_logger sha256sum shasum
  printf '#!/usr/bin/env bash\necho "curl $*" >>"${CALL_LOG}"\necho "echo downloaded script ran >>\\"${CALL_LOG}\\"" >downloadIstioCandidate.sh\n' \
    >"${STUB_BIN}/curl"
  printf '#!/usr/bin/env bash\necho "${FAKE_UNAME}"\n' >"${STUB_BIN}/uname"
  chmod +x "${STUB_BIN}/curl" "${STUB_BIN}/uname"
}

run_download_istio() {
  run --separate-stderr env PATH="${STUB_BIN}:${PATH}" CALL_LOG="${CALL_LOG}" "$@" \
    bash -c 'cd "'"${FAKE_REPO}"'" && bash multi_cluster/tools/download_istio.sh'
}

@test "macOS verifies the checksum with shasum" {
  run_download_istio FAKE_UNAME=Darwin

  assert_succeeded
  grep -qx 'shasum -a 256 --check' "${CALL_LOG}"
  [ "$(calls_matching '^sha256sum')" -eq 0 ]
}

@test "Linux verifies the checksum with sha256sum" {
  run_download_istio FAKE_UNAME=Linux

  assert_succeeded
  grep -qx 'sha256sum --check' "${CALL_LOG}"
  [ "$(calls_matching '^shasum')" -eq 0 ]
}

@test "a checksum mismatch stops the script before the downloaded script runs" {
  printf '#!/usr/bin/env bash\nexit 1\n' >"${STUB_BIN}/sha256sum"

  run_download_istio FAKE_UNAME=Linux

  [ "$status" -ne 0 ]
  [ "$(calls_matching 'downloaded script ran')" -eq 0 ]
}
