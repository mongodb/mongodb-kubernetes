#!/usr/bin/env bats

setup() {
    export FIXTURE_ROOT="${BATS_TEST_TMPDIR}/fixture"
    export CAPTURE_FILE="${FIXTURE_ROOT}/arguments"
    export MMS_HOME="${FIXTURE_ROOT}/home"
    export MMS_LOG_DIR="${FIXTURE_ROOT}/logs"
    export MMS_DOWNLOAD_BASE="${FIXTURE_ROOT}/download"
    export BASE_URL="https://ops-manager.example.com"
    export GROUP_ID="project"
    export MDB_STATIC_CONTAINERS_ARCHITECTURE=""
    unset MDB_AGENT_HOSTNAME_OVERRIDE
    export AGENT_FLAGS="-logLevel=DEBUG,"
    export MDB_LOG_FILE_AUTOMATION_AGENT_VERBOSE="${MMS_LOG_DIR}/verbose.log"
    export MDB_LOG_FILE_AUTOMATION_AGENT="${MMS_LOG_DIR}/agent.log"
    export MDB_LOG_FILE_MONITORING_AGENT="${MMS_LOG_DIR}/monitoring.log"
    export MDB_LOG_FILE_BACKUP_AGENT="${MMS_LOG_DIR}/backup.log"
    export MDB_LOG_FILE_MONGODB="${MMS_LOG_DIR}/mongodb.log"
    export MDB_LOG_FILE_MONGODB_AUDIT="${MMS_LOG_DIR}/audit.log"
    mkdir -p "${FIXTURE_ROOT}"/{bin,scripts/config,agent,tools,data,journal,tmp} "${MMS_HOME}/files" "${MMS_DOWNLOAD_BASE}" "${MMS_LOG_DIR}"

    cat > "${FIXTURE_ROOT}/scripts/agent-launcher-lib.sh" <<'LIB'
json_log() { cat; }
script_log() { :; }
ensure_certs_symlinks() { :; }
download_agent() { :; }
cleanup() { :; }
LIB
    printf '#!/bin/sh\nexit 0\n' > "${FIXTURE_ROOT}/bin/tail"
    printf '#!/bin/sh\nprintf "0\\n"\n' > "${FIXTURE_ROOT}/bin/id"
    printf '#!/bin/sh\nprintf "root 1 mongodb_marker\\n"\n' > "${FIXTURE_ROOT}/bin/ps"
    printf '#!/bin/sh\nprintf "pod-0\\n"\n' > "${FIXTURE_ROOT}/bin/hostname"
    printf '#!/bin/sh\nprintf "%%s\\n" "$@" > "$CAPTURE_FILE"\n' > "${FIXTURE_ROOT}/agent/mongodb-agent"
    cp "${FIXTURE_ROOT}/agent/mongodb-agent" "${MMS_HOME}/files/mongodb-mms-automation-agent"
    printf '#!/bin/sh\nexit 0\n' > "${FIXTURE_ROOT}/setup-agent-files.sh"
    touch "${FIXTURE_ROOT}/tools/"{mongoimport,mongodump,mongorestore,mongoexport}
    chmod +x "${FIXTURE_ROOT}/bin/"* "${FIXTURE_ROOT}/agent/mongodb-agent" "${MMS_HOME}/files/mongodb-mms-automation-agent" "${FIXTURE_ROOT}/setup-agent-files.sh"
    export PATH="${FIXTURE_ROOT}/bin:${PATH}"

    # Rewrite container-only filesystem roots in test copies; execute the real startup control flow.
    python3 - <<'PY'
import os
import pathlib
import re

root = pathlib.Path(os.environ["FIXTURE_ROOT"])
source = pathlib.Path("docker/mongodb-kubernetes-init-database/content/agent-launcher.sh").read_text()
source = re.sub(r"/(opt/scripts|agent|tools|proc|data|tmp)(?=/)|/journal\b",
                lambda m: str(root / ("scripts" if m.group(1) == "opt/scripts" else m.group(0).strip("/"))), source)
(root / "scripts/agent-launcher.sh").write_text(source)
shim = pathlib.Path("docker/mongodb-agent/agent-launcher-shim.sh").read_text()
shim = shim.replace("/opt/scripts", str(root / "scripts"))
shim = shim.replace("/usr/local/bin/setup-agent-files.sh", str(root / "setup-agent-files.sh"))
(root / "shim.sh").write_text(shim)
PY
    chmod +x "${FIXTURE_ROOT}/scripts/agent-launcher.sh" "${FIXTURE_ROOT}/shim.sh"
}

@test "native settings bypass legacy flag assembly and API-key reading" {
    run bash "${FIXTURE_ROOT}/scripts/agent-launcher.sh"
    [ "${status}" -eq 0 ]
    [ "$(cat "${CAPTURE_FILE}")" = $'-f\n/etc/mongodb-agent/automation-agent.conf' ]
    [[ "${output}" != *"agentApiKey"* ]]
}

@test "pod arguments do not change launcher-owned settings" {
    run bash "${FIXTURE_ROOT}/scripts/agent-launcher.sh" "-overrideLocalHost=pod-0.example.com" -f /settings/agent.conf
    [ "${status}" -eq 0 ]
    [ "$(cat "${CAPTURE_FILE}")" = $'-f\n/etc/mongodb-agent/automation-agent.conf' ]
    [[ "${output}" != *"agentApiKey"* ]]
}

@test "native settings work when AGENT_FLAGS is unset" {
    unset AGENT_FLAGS
    run bash "${FIXTURE_ROOT}/scripts/agent-launcher.sh"
    [ "${status}" -eq 0 ]
    [ "$(cat "${CAPTURE_FILE}")" = $'-f\n/etc/mongodb-agent/automation-agent.conf' ]
}

@test "static shim bootstraps the launcher without positional arguments" {
    export MDB_STATIC_CONTAINERS_ARCHITECTURE=true
    export MDB_AGENT_HOSTNAME_OVERRIDE="pod-0.example.com"
    run bash "${FIXTURE_ROOT}/shim.sh"
    [ "${status}" -eq 0 ]
    [ "$(cat "${CAPTURE_FILE}")" = $'-f\n/etc/mongodb-agent/automation-agent.conf\n-overrideLocalHost=pod-0.example.com' ]
}

@test "hostname ConfigMap supplies external and multicluster hostnames in both architectures" {
    for architecture in "" true; do
        export MDB_STATIC_CONTAINERS_ARCHITECTURE="${architecture}"
        for hostname in "pod-0.example.com" "pod-0-svc.ns.svc.custom.local" ""; do
            printf '%s\n' "${hostname}" > "${FIXTURE_ROOT}/scripts/config/pod-0"
            run bash "${FIXTURE_ROOT}/scripts/agent-launcher.sh"
            [ "${status}" -eq 0 ]
            [ "$(cat "${CAPTURE_FILE}")" = "$(printf '%s\n' -f /etc/mongodb-agent/automation-agent.conf "-overrideLocalHost=${hostname}")" ]
        done
    done
}

@test "explicit custom hostname wins over the generated hostname without shell evaluation" {
    printf 'generated.example.com\n' > "${FIXTURE_ROOT}/scripts/config/pod-0"
    export MDB_AGENT_HOSTNAME_OVERRIDE='$(not-a-command); $HOME, "host"'
    run bash "${FIXTURE_ROOT}/scripts/agent-launcher.sh"
    [ "${status}" -eq 0 ]
    [ "$(cat "${CAPTURE_FILE}")" = "$(printf '%s\n' -f /etc/mongodb-agent/automation-agent.conf "-overrideLocalHost=${MDB_AGENT_HOSTNAME_OVERRIDE}")" ]
}

@test "explicit empty custom hostname suppresses the generated hostname" {
    printf 'generated.example.com\n' > "${FIXTURE_ROOT}/scripts/config/pod-0"
    export MDB_AGENT_HOSTNAME_OVERRIDE=""
    run bash "${FIXTURE_ROOT}/scripts/agent-launcher.sh"
    [ "${status}" -eq 0 ]
    [ "$(cat "${CAPTURE_FILE}")" = $'-f\n/etc/mongodb-agent/automation-agent.conf\n-overrideLocalHost=' ]
}
