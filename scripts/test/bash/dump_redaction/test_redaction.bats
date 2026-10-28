#!/usr/bin/env bats
#
# Tests for secret redaction in the e2e diagnostic dump.
#
# Run via: make test-bash suite=dump_redaction

setup() {
    PROJECT_DIR="$(git rev-parse --show-toplevel)"
    cd "${PROJECT_DIR}" || exit 1

    secret_file="${BATS_TEST_TMPDIR}/secret.json"
    cat > "${secret_file}" <<'EOF'
{
  "publicApiKey": "pub-0000",
  "privateApiKey": "priv-1111",
  "user": "mongo-user",
  "password": "hunter2",
  "apiToken": "tok-2222",
  "ca-pem": "-----BEGIN CERTIFICATE-----MIIB-----END CERTIFICATE-----",
  "tls.crt": "cert-3333",
  "tls.key": "key-4444",
  "clientCertificate": "client-cert-5555",
  "someCredential": "cred-6666"
}
EOF
}

# redact runs the redaction function in a child shell, because the dump script
# sets `set +e` when sourced and that would disable errexit in the test shell.
redact() {
    bash -c 'source scripts/evergreen/e2e/dump_diagnostic_information.sh; redact_secret_values' < "${1}"
}

@test "credential values are redacted and the keys stay visible" {
    run redact "${secret_file}"
    [ "${status}" -eq 0 ]
    [[ "${output}" == *'"publicApiKey": "<redacted>"'* ]]
    [[ "${output}" == *'"password": "<redacted>"'* ]]
    [[ "${output}" != *"pub-0000"* ]]
    [[ "${output}" != *"priv-1111"* ]]
    [[ "${output}" != *"hunter2"* ]]
    [[ "${output}" != *"tok-2222"* ]]
    [[ "${output}" != *"key-4444"* ]]
    [[ "${output}" != *"cred-6666"* ]]
}

@test "usernames and public certificate values are kept" {
    run redact "${secret_file}"
    [ "${status}" -eq 0 ]
    [[ "${output}" == *'"user": "mongo-user"'* ]]
    [[ "${output}" == *"cert-3333"* ]]
    [[ "${output}" == *"client-cert-5555"* ]]
    [[ "${output}" == *"BEGIN CERTIFICATE"* ]]
}
