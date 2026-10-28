#!/usr/bin/env bats
#
# Tests for the diagnostic dump hardening: distinct StatefulSet files and
# visible kubectl cp failures.
#
# Run via: make test-bash suite=dump_hardening

setup() {
    PROJECT_DIR="$(git rev-parse --show-toplevel)"
    cd "${PROJECT_DIR}" || exit 1
}

@test "StatefulSet describe and YAML use distinct files" {
    run bash -c '
        set +e
        project_dir="$1"
        tmpdir="$2"
        cd "${project_dir}" || exit 1
        source scripts/evergreen/e2e/dump_diagnostic_information.sh
        cd "${tmpdir}" || exit 1
        mkdir -p logs
        kubectl() {
            case "$*" in
                *" get sts "*"--no-headers"*) echo "statefulset.apps/my-sts" ;;
                *" describe sts"*) printf "Name: my-sts\nNamespace: ns\n" ;;
                *" get -o yaml sts"*) printf "apiVersion: apps/v1\nkind: StatefulSet\nmetadata:\n  name: my-sts\n" ;;
                *) return 0 ;;
            esac
        }
        run_with_timeout() { "$@"; }
        dump_namespace "ctx" "ns" "test"
    ' _ "${PROJECT_DIR}" "${BATS_TEST_TMPDIR}"

    [ "${status}" -eq 0 ]
    [ -f "${BATS_TEST_TMPDIR}/logs/test_ns_z_statefulsets_describe.txt" ]
    [ -f "${BATS_TEST_TMPDIR}/logs/test_ns_z_statefulsets.txt" ]
    grep -q "Name: my-sts" "${BATS_TEST_TMPDIR}/logs/test_ns_z_statefulsets_describe.txt"
    grep -q "kind: StatefulSet" "${BATS_TEST_TMPDIR}/logs/test_ns_z_statefulsets.txt"
    run grep -q "kind: StatefulSet" "${BATS_TEST_TMPDIR}/logs/test_ns_z_statefulsets_describe.txt"
    [ "${status}" -ne 0 ]
}

@test "cp_from_pod warns with pod, container, file, and error" {
    run bash -c '
        set +e
        cd "$1" || exit 1
        source scripts/evergreen/e2e/dump_diagnostic_information.sh
        kubectl() { echo "pod my-replica-set-0 not found" >&2; return 1; }
        run_with_timeout() { "$@"; }
        cp_from_pod "ctx" "ns" "my-replica-set-0" "" "/var/log/mongodb-mms-automation/automation-agent.log" "logs/out.log"
    ' _ "${PROJECT_DIR}"

    [ "${status}" -eq 0 ]
    [[ "${output}" == *"WARNING: kubectl cp failed for pod my-replica-set-0"* ]]
    [[ "${output}" == *"container default"* ]]
    [[ "${output}" == *"/var/log/mongodb-mms-automation/automation-agent.log"* ]]
    [[ "${output}" == *"pod my-replica-set-0 not found"* ]]
}

@test "cp_from_pod reports a timeout" {
    run bash -c '
        set +e
        cd "$1" || exit 1
        source scripts/evergreen/e2e/dump_diagnostic_information.sh
        kubectl() { return 124; }
        run_with_timeout() { "$@"; }
        cp_from_pod "ctx" "ns" "my-replica-set-1" "mongodb-agent" "/var/log/mongodb-mms-automation/automation-agent.log" "logs/out.log"
    ' _ "${PROJECT_DIR}"

    [ "${status}" -eq 0 ]
    [[ "${output}" == *"container mongodb-agent"* ]]
    [[ "${output}" == *"timed out after 30s"* ]]
}
