#!/usr/bin/env bats
#
# Tests for the e2e diagnostic dump. The dump runs only after a failed task,
# so these are the only automated checks for its output files.
#
# Run via: make test-bash suite=dump_diagnostic

setup() {
    cd "$(git rev-parse --show-toplevel)" || exit 1
    source scripts/evergreen/e2e/dump_diagnostic_information.sh
    set -e   # the dump script starts with `set +e`
    # The real timeout execs binaries and cannot see the kubectl function stub.
    run_with_timeout() { "$@"; }
    cd "${BATS_TEST_TMPDIR}" || exit 1
    mkdir -p logs
}

@test "StatefulSet describe and YAML use distinct files" {
    kubectl() {
        case "$*" in
            *" get sts "*"--no-headers"*) echo "statefulset.apps/my-sts" ;;
            *" describe sts"*) echo "Name: my-sts" ;;
            *" get -o yaml sts"*) echo "kind: StatefulSet" ;;
        esac
    }

    run dump_namespace "ctx" "ns" "test"

    grep -q "Name: my-sts" "logs/test_ns_z_statefulsets_describe.txt"
    grep -q "kind: StatefulSet" "logs/test_ns_z_statefulsets.txt"
}

@test "cp_from_pod warns with pod, container, file, and error" {
    kubectl() { echo "pod my-replica-set-0 not found" >&2; return 1; }

    run cp_from_pod "ctx" "ns" "my-replica-set-0" "" "/var/log/agent.log" "logs/out.log"

    [ "${status}" -eq 0 ]
    [[ "${output}" == *"WARNING: kubectl cp failed for pod my-replica-set-0"* ]]
    [[ "${output}" == *"container default"* ]]
    [[ "${output}" == *"/var/log/agent.log"* ]]
    [[ "${output}" == *"pod my-replica-set-0 not found"* ]]
}

@test "proxy state is collected only for Pods with the socket volume" {
    kubectl() {
        case "$*" in
            *".status.containerStatuses"*) echo 0 ;;
            *" get pod "*"proxy-pod"*) printf 'mongodb-agent|mongodb-proxy-socket' ;;
            *" get pod "*"other-pod"*) printf 'sidecar|' ;;
            *" exec "*"curl "*) echo '{"state":"running"}' ;;
            *" exec "*) return 1 ;;
        esac
    }

    dump_pod_logs "ctx" "proxy-pod" "ns" "test_"
    dump_pod_logs "ctx" "other-pod" "ns" "test_"

    [ "$(cat logs/test_proxy-pod-proxy-state.json)" = '{"state":"running"}' ]
    [ ! -e logs/test_other-pod-proxy-state.json ]
}

@test "Pod spec lookup failure is reported without creating proxy state" {
    kubectl() {
        case "$*" in
            *" get pod "*) return 1 ;;
            *" exec "*) return 1 ;;
        esac
    }

    run dump_pod_logs "ctx" "missing-pod" "ns" "test_"

    [[ "${output}" == *"WARNING: could not read Pod spec for ns/missing-pod"* ]]
    [ ! -e logs/test_missing-pod-proxy-state.json ]
}

@test "credential values are redacted and usernames and certificates stay" {
    run redact_secret_values <<< '{"publicApiKey":"pub-0000","password":"hunter2","connectionString.standard":"mongodb://user:pass@host","user":"mongo-user","ca-pem":"-----BEGIN CERTIFICATE-----MIIB-----END CERTIFICATE-----"}'

    [ "${status}" -eq 0 ]
    [[ "${output}" == *'"publicApiKey": "<redacted>"'* ]]
    [[ "${output}" == *'"password": "<redacted>"'* ]]
    [[ "${output}" == *'"connectionString.standard": "<redacted>"'* ]]
    [[ "${output}" == *'"user": "mongo-user"'* ]]
    [[ "${output}" == *"BEGIN CERTIFICATE"* ]]
    [[ "${output}" != *"user:pass@host"* ]]
}

@test "license and release receipt values are redacted" {
    run redact_secret_values <<< '{"license":"signed-license","release-receipt":"signed-receipt"}'

    [ "${status}" -eq 0 ]
    [[ "${output}" == *'"license": "<redacted>"'* ]]
    [[ "${output}" == *'"release-receipt": "<redacted>"'* ]]
    [[ "${output}" != *"signed-"* ]]
}
