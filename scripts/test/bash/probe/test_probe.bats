#!/usr/bin/env bats

# The probe runs in the agent container. ps is replaced by a canned process list so every architecture and
# process combination can be checked without starting containers.

setup() {
    export FIXTURE_ROOT="${BATS_TEST_TMPDIR}/fixture"
    mkdir -p "${FIXTURE_ROOT}/bin"
    export PATH="${FIXTURE_ROOT}/bin:${PATH}"
    cp docker/mongodb-kubernetes-init-database/content/probe.sh "${FIXTURE_ROOT}/probe.sh"
    chmod +x "${FIXTURE_ROOT}/probe.sh"
    unset MDB_STATIC_CONTAINERS_ARCHITECTURE

    cat > "${FIXTURE_ROOT}/bin/ps" <<'PS'
#!/usr/bin/env bash
cat "${FIXTURE_ROOT}/ps.out"
PS
    chmod +x "${FIXTURE_ROOT}/bin/ps"
    : > "${FIXTURE_ROOT}/ps.out"
}

processes() {
    printf '%s\n' "$@" > "${FIXTURE_ROOT}/ps.out"
}

@test "static-ga stays alive while the Agent runs" {
    export MDB_STATIC_CONTAINERS_ARCHITECTURE=static-ga
    processes "/agent/mongodb-agent -f /etc/mongodb-agent/automation-agent.conf"
    run bash "${FIXTURE_ROOT}/probe.sh"
    [ "${status}" -eq 0 ]
}

@test "static-ga does not treat a mongod from another container as a live Agent" {
    export MDB_STATIC_CONTAINERS_ARCHITECTURE=static-ga
    # Nothing in the Agent container but the probe itself. mongod runs in the database container, and a matching
    # "mongod" line must not make this container look healthy.
    processes "bash ${FIXTURE_ROOT}/probe.sh" "mongod -f /data/mongod.conf"
    run bash "${FIXTURE_ROOT}/probe.sh"
    [ "${status}" -ne 0 ]
}

@test "static-ga does not match the proxy binary as an Agent" {
    export MDB_STATIC_CONTAINERS_ARCHITECTURE=static-ga
    processes "/mnt/proxy/proxy -listen unix:///var/run/mongodb-proxy/proxy.sock"
    run bash "${FIXTURE_ROOT}/probe.sh"
    [ "${status}" -ne 0 ]
}

@test "the non-static architecture keeps the database process check" {
    processes "mongodb-mms-automation-agent -f /etc/mongodb-agent/automation-agent.conf"
    run bash "${FIXTURE_ROOT}/probe.sh"
    [ "${status}" -eq 0 ]

    processes "mongod -f /data/mongod.conf"
    run bash "${FIXTURE_ROOT}/probe.sh"
    [ "${status}" -eq 0 ]
}
