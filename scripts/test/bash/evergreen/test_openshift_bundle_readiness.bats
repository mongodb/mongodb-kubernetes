#!/usr/bin/env bats

setup() {
  HELPER="$BATS_TEST_DIRNAME/../../../../scripts/evergreen/ensure_openshift_bundle_ready.sh"
  TEST_WORKDIR=$(mktemp -d "$BATS_TEST_TMPDIR/openshift-bundle.XXXXXX")
  BIN_DIR="$TEST_WORKDIR/bin"
  PREPARE_MARKER="$TEST_WORKDIR/prepared"
  AWS_MARKER="$TEST_WORKDIR/uploaded"
  mkdir -p "$BIN_DIR"
  printf '{"mongodbOperator":"1.2.3"}\n' >"$TEST_WORKDIR/release.json"

  cat >"$BIN_DIR/jq" <<'EOF'
#!/usr/bin/env bash
cat >/dev/null
printf '%s\n' '1.2.3'
EOF
  cat >"$BIN_DIR/curl" <<'EOF'
#!/usr/bin/env bash
if [[ "${MOCK_CURL_EXIT:-0}" != 0 ]]; then
  exit "$MOCK_CURL_EXIT"
fi
printf '%s' "${MOCK_HTTP_STATUS:-000}"
EOF
  cat >"$BIN_DIR/aws" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >"$AWS_MARKER"
EOF
  cat >"$TEST_WORKDIR/prepare-bundle" <<'EOF'
#!/usr/bin/env bash
touch "$PREPARE_MARKER"
mkdir -p bundle
touch "bundle/mck-operator-certified-${VERSION}.tgz"
EOF
  chmod +x "$BIN_DIR/jq" "$BIN_DIR/curl" "$BIN_DIR/aws" "$TEST_WORKDIR/prepare-bundle"

  export PATH="$BIN_DIR:$PATH"
  export AWS_MARKER PREPARE_MARKER
  export PREPARE_BUNDLE_SCRIPT="$TEST_WORKDIR/prepare-bundle"
  export enterprise_aws_access_key_id=test-access-key
  export enterprise_aws_secret_access_key=test-secret-key
  cd "$TEST_WORKDIR" || exit
}

@test "existing bundle skips preparation and upload" {
  run env MOCK_HTTP_STATUS=200 "$HELPER"

  [ "$status" -eq 0 ]
  [[ "$output" == *"already exists"* ]]
  [ ! -e "$PREPARE_MARKER" ]
  [ ! -e "$AWS_MARKER" ]
}

@test "missing bundle prepares and uploads" {
  run env MOCK_HTTP_STATUS=404 "$HELPER"

  [ "$status" -eq 0 ]
  [ -e "$PREPARE_MARKER" ]
  [ -e "$AWS_MARKER" ]
  [[ "$(cat "$AWS_MARKER")" == *"s3://operator-e2e-bundles/bundles/mck-operator-certified-1.2.3.tgz"* ]]
}

@test "curl network failure fails without preparation or upload" {
  run env MOCK_CURL_EXIT=7 "$HELPER"

  [ "$status" -eq 7 ]
  [[ "$output" == *"Unable to check certified bundle"* ]]
  [ ! -e "$PREPARE_MARKER" ]
  [ ! -e "$AWS_MARKER" ]
}

@test "unexpected HTTP status does not trigger upload" {
  run env MOCK_HTTP_STATUS=403 "$HELPER"

  [ "$status" -eq 1 ]
  [[ "$output" == *"Unexpected HTTP status"* ]]
  [ ! -e "$PREPARE_MARKER" ]
  [ ! -e "$AWS_MARKER" ]
}
