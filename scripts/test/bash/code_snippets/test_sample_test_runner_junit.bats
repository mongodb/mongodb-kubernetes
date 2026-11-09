#!/usr/bin/env bats
#
# Tests for the Go test output that sample_test_runner.sh writes for Evergreen.
# Evergreen reads logs/snippets.junit.suite with gotest.parse_files.
#
# Run via: make test-bash suite=code_snippets

setup() {
    project_dir="$(git rev-parse --show-toplevel)"
    test_root="${project_dir}/logs/test-sample-test-runner-$$"
    rm -rf "${test_root}"
    phase_dir="${test_root}/docs/search/09-test-phase"
    junit_dir="${test_root}/junit"
    suite="${junit_dir}/snippets.junit.suite"
    phase_name="logs/test-sample-test-runner-$$/docs/search/09-test-phase"
    mkdir -p "${phase_dir}/code_snippets" "${junit_dir}"

    cat > "${phase_dir}/phase.sh" <<EOF
#!/usr/bin/env bash
set -eou pipefail
script_dir="${phase_dir}"
export script_dir
source "${project_dir}/scripts/code_snippets/sample_test_runner.sh"
cd "\${script_dir}"
prepare_snippets
"\$@"
EOF
    export SNIPPETS_JUNIT_DIR="${junit_dir}"
}

teardown() {
    if [[ -n "${test_root:-}" ]]; then
        rm -rf "${test_root}"
    fi
}

write_snippet() {
    local name=$1
    shift
    printf '%s\n' "$@" > "${phase_dir}/code_snippets/${name}"
}

@test "passing snippets append one block each and a repeat call adds none" {
    write_snippet 01_pass.sh 'echo hello'
    write_snippet 02_pass.sh 'echo world'
    run bash "${phase_dir}/phase.sh" run 01_pass.sh
    [ "${status}" -eq 0 ]
    run bash "${phase_dir}/phase.sh" run 02_pass.sh
    [ "${status}" -eq 0 ]
    run bash "${phase_dir}/phase.sh" run 01_pass.sh
    [ "${status}" -eq 0 ]

    [ "$(grep -c '^=== RUN ' "${suite}")" -eq 2 ]
    [ "$(wc -l < "${suite}")" -eq 4 ]
    [ "$(sed -n '1p' "${suite}")" = "=== RUN   ${phase_name}/01_pass.sh" ]
    [ "$(sed -n '3p' "${suite}")" = "=== RUN   ${phase_name}/02_pass.sh" ]
    grep -qE "^--- PASS: ${phase_name}/01_pass\.sh \([0-9]+s\)$" "${suite}"
    grep -qE "^--- PASS: ${phase_name}/02_pass\.sh \([0-9]+s\)$" "${suite}"
}

@test "failing snippet writes only the two generated lines" {
    write_snippet 03_fail.sh \
        'printf "%s\n" "boom <tag> &" "=== RUN   fake" "--- PASS: fake (1s)" >&2' \
        'exit 3'
    run bash "${phase_dir}/phase.sh" run 03_fail.sh
    [ "${status}" -eq 3 ]

    [ "$(wc -l < "${suite}")" -eq 2 ]
    grep -qxF "=== RUN   ${phase_name}/03_fail.sh" "${suite}"
    grep -qE "^--- FAIL: ${phase_name}/03_fail\.sh \([0-9]+s\)$" "${suite}"
    run grep -q 'fake' "${suite}"
    [ "${status}" -ne 0 ]
}

@test "run_for_output writes a passing block and the output file" {
    write_snippet 03_output.sh 'echo output-value'
    run bash "${phase_dir}/phase.sh" run_for_output 03_output.sh
    [ "${status}" -eq 0 ]

    grep -qxF "=== RUN   ${phase_name}/03_output.sh" "${suite}"
    grep -qE "^--- PASS: ${phase_name}/03_output\.sh \([0-9]+s\)$" "${suite}"
    [ "$(cat "${phase_dir}/output/03_output.out")" = "output-value" ]
}
