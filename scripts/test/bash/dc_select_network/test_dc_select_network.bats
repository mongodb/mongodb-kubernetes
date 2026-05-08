#!/usr/bin/env bats
#
# Integration tests for scripts/dev/dc_select_network.sh
#
# The registry (~/.cache/mck-devc/net-prefix-registry by default) is redirected
# to a per-test temp dir via MCK_DEVC_REGISTRY_DIR. Docker is stubbed via a
# sandboxed PATH so the test doesn't depend on a live docker daemon.
#
# Requirements: bats-core (brew install bats-core)
# Run via: make test-bash suite=dc_select_network
#         scripts/test/bash/run.sh dc_select_network

setup() {
    PROJECT_DIR="$(git rev-parse --show-toplevel)"
    SCRIPT="${PROJECT_DIR}/scripts/dev/dc_select_network.sh"

    # Per-test sandbox for the registry.
    TEST_DIR="$(mktemp -d "${BATS_TMPDIR:-/tmp}/dc-select-net.XXXXXX")"
    export MCK_DEVC_REGISTRY_DIR="${TEST_DIR}/registry"

    # Stub `docker` so the script doesn't depend on a live daemon. Override
    # via DOCKER_NETWORK_SUBNETS (newline-separated CIDR list) per test.
    STUB_BIN="${TEST_DIR}/bin"
    mkdir -p "${STUB_BIN}"
    cat > "${STUB_BIN}/docker" <<'EOF'
#!/usr/bin/env bash
# Tiny stub: emits the JSON shape the real script reads.
# Inputs:
#   docker network ls --format '{{.Name}}'  -> one line per fake net name.
#   docker network inspect <name> --format '{{range .IPAM.Config}}{{.Subnet}}{{"\n"}}{{end}}'
#       -> echo the subnet for that name.
# Backed by the env var DOCKER_NETWORK_SUBNETS (newline-separated CIDRs).
case "$1 $2" in
  "network ls")
    i=0
    while IFS= read -r _; do
      i=$((i+1))
      echo "fake-net-${i}"
    done <<< "${DOCKER_NETWORK_SUBNETS:-}"
    ;;
  "network inspect")
    name="$3"
    idx="${name#fake-net-}"
    line=0
    while IFS= read -r subnet; do
      line=$((line+1))
      if [[ "${line}" == "${idx}" ]]; then
        echo "${subnet}"
        break
      fi
    done <<< "${DOCKER_NETWORK_SUBNETS:-}"
    ;;
  *)
    echo "stub-docker: unhandled invocation: $*" >&2
    exit 1
    ;;
esac
EOF
    chmod +x "${STUB_BIN}/docker"
    PATH="${STUB_BIN}:${PATH}"
    export PATH

    # Default: no docker networks consume any 172.X subnet.
    export DOCKER_NETWORK_SUBNETS=""

    # Don't let env-var injection skip our logic.
    unset MCK_DEVC_NET_PREFIX
}

teardown() {
    [[ -n "${TEST_DIR:-}" && -d "${TEST_DIR}" ]] && rm -rf "${TEST_DIR}"
}

# Helper: extract the numeric prefix from "MCK_DEVC_NET_PREFIX=N".
prefix_of() {
    local line="$1"
    echo "${line#MCK_DEVC_NET_PREFIX=}"
}

# ---------------------------------------------------------------------------
# basic allocation
# ---------------------------------------------------------------------------

@test "first run with empty registry returns lowest free prefix and records it" {
    run "${SCRIPT}" --branch-dir alpha
    [ "$status" -eq 0 ]
    [[ "$output" == "MCK_DEVC_NET_PREFIX=16" ]]
    grep -q '^alpha=16$' "${MCK_DEVC_REGISTRY_DIR}/net-prefix-registry"
}

@test "second run for the same branch_dir is idempotent" {
    "${SCRIPT}" --branch-dir alpha >/dev/null
    run "${SCRIPT}" --branch-dir alpha
    [ "$status" -eq 0 ]
    [[ "$output" == "MCK_DEVC_NET_PREFIX=16" ]]
    # Registry must still have exactly one entry for alpha.
    [ "$(grep -c '^alpha=' "${MCK_DEVC_REGISTRY_DIR}/net-prefix-registry")" -eq 1 ]
}

@test "distinct branch_dirs get distinct prefixes" {
    run "${SCRIPT}" --branch-dir alpha
    [ "$status" -eq 0 ]
    a="$(prefix_of "$output")"

    run "${SCRIPT}" --branch-dir beta
    [ "$status" -eq 0 ]
    b="$(prefix_of "$output")"

    [ "$a" != "$b" ]
    [ "$a" -ge 16 ] && [ "$a" -le 31 ]
    [ "$b" -ge 16 ] && [ "$b" -le 31 ]
}

@test "registry is consulted before docker scan (skips registry-recorded prefix)" {
    # Pre-seed: alpha already has 16, but docker reports nothing.
    mkdir -p "${MCK_DEVC_REGISTRY_DIR}"
    echo 'alpha=16' > "${MCK_DEVC_REGISTRY_DIR}/net-prefix-registry"

    run "${SCRIPT}" --branch-dir beta
    [ "$status" -eq 0 ]
    # Lowest free is 17, since 16 is taken by the alpha entry.
    [[ "$output" == "MCK_DEVC_NET_PREFIX=17" ]]
}

@test "docker-occupied subnet is also avoided" {
    DOCKER_NETWORK_SUBNETS=$'172.16.0.0/16\n172.17.0.0/16' \
        run "${SCRIPT}" --branch-dir alpha
    [ "$status" -eq 0 ]
    [[ "$output" == "MCK_DEVC_NET_PREFIX=18" ]]
}

@test "exhaustion (registry full) errors with exit 1" {
    mkdir -p "${MCK_DEVC_REGISTRY_DIR}"
    : > "${MCK_DEVC_REGISTRY_DIR}/net-prefix-registry"
    for x in $(seq 16 31); do
        echo "branch${x}=${x}" >> "${MCK_DEVC_REGISTRY_DIR}/net-prefix-registry"
    done

    run "${SCRIPT}" --branch-dir overflow
    [ "$status" -eq 1 ]
    [[ "$output" == *"no free 172."*"subnet available"* ]]
}

# ---------------------------------------------------------------------------
# --free mode (P9)
# ---------------------------------------------------------------------------

@test "--free removes the registry entry for the branch" {
    "${SCRIPT}" --branch-dir alpha >/dev/null
    "${SCRIPT}" --branch-dir beta  >/dev/null
    grep -q '^alpha=' "${MCK_DEVC_REGISTRY_DIR}/net-prefix-registry"
    grep -q '^beta='  "${MCK_DEVC_REGISTRY_DIR}/net-prefix-registry"

    run "${SCRIPT}" --free alpha
    [ "$status" -eq 0 ]
    ! grep -q '^alpha=' "${MCK_DEVC_REGISTRY_DIR}/net-prefix-registry"
    grep -q '^beta='  "${MCK_DEVC_REGISTRY_DIR}/net-prefix-registry"
}

@test "--free is a no-op when entry doesn't exist" {
    "${SCRIPT}" --branch-dir alpha >/dev/null
    before="$(cat "${MCK_DEVC_REGISTRY_DIR}/net-prefix-registry")"

    run "${SCRIPT}" --free does-not-exist
    [ "$status" -eq 0 ]
    after="$(cat "${MCK_DEVC_REGISTRY_DIR}/net-prefix-registry")"
    [ "$before" = "$after" ]
}

@test "--free of last entry frees the slot for re-allocation" {
    "${SCRIPT}" --branch-dir alpha >/dev/null
    "${SCRIPT}" --free alpha       >/dev/null

    # alpha gone — beta should now get prefix 16, the lowest.
    run "${SCRIPT}" --branch-dir beta
    [ "$status" -eq 0 ]
    [[ "$output" == "MCK_DEVC_NET_PREFIX=16" ]]
}

@test "--branch-dir and --free together error out" {
    run "${SCRIPT}" --branch-dir alpha --free alpha
    [ "$status" -eq 2 ]
    [[ "$output" == *"mutually exclusive"* ]]
}

# ---------------------------------------------------------------------------
# concurrency (lock)
# ---------------------------------------------------------------------------

@test "concurrent invocations get distinct prefixes" {
    # Background two invocations under a synchronization barrier so both
    # cross the lock window in quick succession. The lock is mkdir-based;
    # the second has to wait until the first releases.
    out1="${TEST_DIR}/out1"
    out2="${TEST_DIR}/out2"
    "${SCRIPT}" --branch-dir alpha > "${out1}" &
    pid1=$!
    "${SCRIPT}" --branch-dir beta  > "${out2}" &
    pid2=$!
    wait "${pid1}"
    wait "${pid2}"

    a="$(prefix_of "$(cat "${out1}")")"
    b="$(prefix_of "$(cat "${out2}")")"

    [ -n "$a" ] && [ -n "$b" ]
    [ "$a" != "$b" ]
    grep -q "^alpha=${a}$" "${MCK_DEVC_REGISTRY_DIR}/net-prefix-registry"
    grep -q "^beta=${b}$"  "${MCK_DEVC_REGISTRY_DIR}/net-prefix-registry"
}

@test "lock dir is released after a successful run" {
    "${SCRIPT}" --branch-dir alpha >/dev/null
    [[ ! -d "${MCK_DEVC_REGISTRY_DIR}/net-prefix-registry.lock.d" ]]
}

# ---------------------------------------------------------------------------
# trust-the-caller mode (MCK_DEVC_NET_PREFIX env var)
# ---------------------------------------------------------------------------

@test "MCK_DEVC_NET_PREFIX env var is honored and skips the registry" {
    MCK_DEVC_NET_PREFIX=20 run "${SCRIPT}" --branch-dir alpha
    [ "$status" -eq 0 ]
    [[ "$output" == "MCK_DEVC_NET_PREFIX=20" ]]
    # Nothing recorded — caller takes responsibility.
    [[ ! -f "${MCK_DEVC_REGISTRY_DIR}/net-prefix-registry" \
       || -z "$(cat "${MCK_DEVC_REGISTRY_DIR}/net-prefix-registry")" ]]
}

@test "MCK_DEVC_NET_PREFIX out of range errors" {
    MCK_DEVC_NET_PREFIX=99 run "${SCRIPT}"
    [ "$status" -eq 1 ]
    [[ "$output" == *"is not in"* ]]
}
