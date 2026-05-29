#!/opt/homebrew/bin/bash
# Tests for agent-launcher-lib.sh – run with: bash agent-launcher-lib_test.sh
# Requires bash 4+ (uses &>> redirection from agent-launcher-lib.sh).
set -eou pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

export MDB_LOG_FILE_AGENT_LAUNCHER_SCRIPT="/dev/null"
source "${SCRIPT_DIR}/agent-launcher-lib.sh"

pass=0
fail=0

assert_eq() {
    local desc="$1" expected="$2" actual="$3"
    if [ "${expected}" = "${actual}" ]; then
        echo "PASS: ${desc}"
        (( pass++ )) || true
    else
        echo "FAIL: ${desc} — expected '${expected}', got '${actual}'"
        (( fail++ )) || true
    fi
}

assert_file_size_le() {
    local desc="$1" file="$2" max_bytes="$3"
    local size
    size=$(stat -c%s "${file}" 2>/dev/null || stat -f%z "${file}")
    if [ "${size}" -le "${max_bytes}" ]; then
        echo "PASS: ${desc} (size=${size})"
        (( pass++ )) || true
    else
        echo "FAIL: ${desc} — size ${size} exceeds ${max_bytes}"
        (( fail++ )) || true
    fi
}

# ── helpers ──────────────────────────────────────────────────────────────────

make_log() {
    local file="$1" size_bytes="$2"
    python3 -c "import sys; sys.stdout.buffer.write(b'x' * ${size_bytes})" > "${file}"
}

# ── tests ─────────────────────────────────────────────────────────────────────

tmp_dir=$(mktemp -d)
trap 'rm -rf "${tmp_dir}"' EXIT

# 1. File below threshold: not modified
t1="${tmp_dir}/t1.log"
make_log "${t1}" 100
rotate_log_if_needed "${t1}" 200
assert_eq "file below threshold is not rotated" "100" "$(stat -c%s "${t1}" 2>/dev/null || stat -f%z "${t1}")"

# 2. File exactly at threshold: not modified (condition is strictly greater-than)
t2="${tmp_dir}/t2.log"
make_log "${t2}" 200
rotate_log_if_needed "${t2}" 200
assert_eq "file at threshold is not rotated" "200" "$(stat -c%s "${t2}" 2>/dev/null || stat -f%z "${t2}")"

# 3. File above threshold: trimmed to keep_bytes (max/2)
t3="${tmp_dir}/t3.log"
make_log "${t3}" 300
rotate_log_if_needed "${t3}" 200
assert_eq "file above threshold is trimmed to keep_bytes" "100" "$(stat -c%s "${t3}" 2>/dev/null || stat -f%z "${t3}")"

# 4. Most-recent content is preserved after rotation
t4="${tmp_dir}/t4.log"
printf '%0.s-' {1..100} > "${t4}"   # 100 bytes of dashes (older data)
printf '%0.s+' {1..100} >> "${t4}"  # 100 bytes of plusses (newer data)
rotate_log_if_needed "${t4}" 100    # max=100, keep=50 → last 50 bytes of plusses
content=$(cat "${t4}")
assert_eq "rotation keeps newest content" "$(printf '%0.s+' {1..50})" "${content}"

# 5. Trailing newlines are preserved (no $(...) stripping)
t5="${tmp_dir}/t5.log"
{
    printf '%0.s-' {1..60}   # 60 bytes older data
    printf 'last line\n'     # 10 bytes newer data with trailing newline
} > "${t5}"
rotate_log_if_needed "${t5}" 40   # max=40, keep=20 → last 20 bytes
last_char=$(tail -c 1 "${t5}" | xxd -p)
assert_eq "trailing newline is preserved after rotation" "0a" "${last_char}"

# 6. Non-existent file: no error
rotate_log_if_needed "${tmp_dir}/does-not-exist.log" 100
echo "PASS: non-existent file does not cause an error"
(( pass++ )) || true

# 7. Temp file is cleaned up after rotation
t7="${tmp_dir}/t7.log"
make_log "${t7}" 300
rotate_log_if_needed "${t7}" 200
assert_eq "temp file is removed after rotation" "" "$(ls "${tmp_dir}/t7.log.rotating" 2>/dev/null || true)"

# ── summary ───────────────────────────────────────────────────────────────────

echo ""
echo "Results: ${pass} passed, ${fail} failed"
[ "${fail}" -eq 0 ]
