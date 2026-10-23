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
    export AGENT_FLAGS="-logLevel=DEBUG,"
    export MDB_LOG_FILE_AUTOMATION_AGENT_VERBOSE="${MMS_LOG_DIR}/verbose.log"
    export MDB_LOG_FILE_AUTOMATION_AGENT="${MMS_LOG_DIR}/agent.log"
    export MDB_LOG_FILE_MONITORING_AGENT="${MMS_LOG_DIR}/monitoring.log"
    export MDB_LOG_FILE_BACKUP_AGENT="${MMS_LOG_DIR}/backup.log"
    export MDB_LOG_FILE_MONGODB="${MMS_LOG_DIR}/mongodb.log"
    export MDB_LOG_FILE_MONGODB_AUDIT="${MMS_LOG_DIR}/audit.log"
    mkdir -p "${FIXTURE_ROOT}"/{bin,scripts,agent,tools,data,journal,tmp} "${MMS_HOME}/files" "${MMS_DOWNLOAD_BASE}" "${MMS_LOG_DIR}"

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
    run bash "${FIXTURE_ROOT}/scripts/agent-launcher.sh" -f "/settings with spaces/agent.conf" "-overrideLocalHost=pod-0.example.com"
    [ "${status}" -eq 0 ]
    [ "$(cat "${CAPTURE_FILE}")" = $'-f\n/settings with spaces/agent.conf\n-overrideLocalHost=pod-0.example.com' ]
    [[ "${output}" != *"agentApiKey"* ]]
}

@test "static shim forwards native settings through bootstrap without extra flags" {
    export MDB_STATIC_CONTAINERS_ARCHITECTURE=true
    run bash "${FIXTURE_ROOT}/shim.sh" -f "/settings with spaces/agent.conf" "-overrideLocalHost=pod-0.example.com"
    [ "${status}" -eq 0 ]
    [ "$(cat "${CAPTURE_FILE}")" = $'-f\n/settings with spaces/agent.conf\n-overrideLocalHost=pod-0.example.com' ]
}

@test "legacy startup remains available for deployment paths not yet converted" {
    mkdir -p "${MMS_HOME}/agent-api-key"
    printf 'legacy-test-key' > "${MMS_HOME}/agent-api-key/agentApiKey"
    run bash "${FIXTURE_ROOT}/scripts/agent-launcher.sh"
    [ "${status}" -eq 0 ]
    arguments="$(cat "${CAPTURE_FILE}")"
    [[ "${arguments}" == *"-mmsBaseUrl=https://ops-manager.example.com"* ]]
    [[ "${arguments}" == *"-mmsApiKey=legacy-test-key"* ]]
    [[ "${arguments}" == *"-logLevel=DEBUG" ]]
}

@test "native settings forward only unresolved runtime defaults without reading credentials" {
    export LOG_LEVEL=TRACE
    export SSL_REQUIRE_VALID_MMS_CERTIFICATES=false
    export SSL_TRUSTED_MMS_SERVER_CERTIFICATE="/runtime CA.pem"
    export MDB_AGENT_RUNTIME_SETTINGS="mmsGroupId,pidfilepath,logLevel,healthCheckFilePath,mmsBaseUrl,tlsRequireValidMMSServerCertificates,httpsCAFile"
    for architecture in "" true; do
        export MDB_STATIC_CONTAINERS_ARCHITECTURE="${architecture}"
        run bash "${FIXTURE_ROOT}/shim.sh" -f "/settings with spaces/agent.conf" "-overrideLocalHost=pod-0.example.com"
        [ "${status}" -eq 0 ] || return 1
        arguments="$(cat "${CAPTURE_FILE}")"
        [[ "${arguments}" == *"-mmsGroupId=project"* ]] || return 1
        [[ "${arguments}" == *"-pidfilepath=${MMS_HOME}/mongodb-mms-automation-agent.pid"* ]] || return 1
        [[ "${arguments}" == *"-logLevel=TRACE"* ]] || return 1
        [[ "${arguments}" == *"-healthCheckFilePath=${MMS_LOG_DIR}/agent-health-status.json"* ]] || return 1
        [[ "${arguments}" == *"-httpsCAFile=/runtime CA.pem"* ]] || return 1
        [[ "${arguments}" == *"-tlsRequireValidMMSServerCertificates=false"* ]] || return 1
        [[ "${arguments}" == *"-mmsBaseUrl=https://ops-manager.example.com"* ]] || return 1
        [[ "${arguments}" != *"-maxLogFileDurationHrs="* ]] || return 1
        [[ "${arguments}" != *"-mmsApiKey="* ]] || return 1
        [[ "${arguments}" == *$'-f\n/settings with spaces/agent.conf\n-overrideLocalHost=pod-0.example.com' ]] || return 1
        [[ "${output}" != *"agentApiKey"* ]] || return 1
    done
}

@test "runtime headless and architecture defaults do not enter the shared file" {
    export BASE_URL=""
    export MULTI_CLUSTER_MODE=true
    export MDB_AGENT_RUNTIME_SETTINGS="cluster,serveStatusPort,operatorMode,useLocalMongoDbTools,binariesFixedPath,ephemeralPortOffset"
    for architecture in "" true; do
        export MDB_STATIC_CONTAINERS_ARCHITECTURE="${architecture}"
        run bash "${FIXTURE_ROOT}/scripts/agent-launcher.sh" -f /settings/agent.conf
        [ "${status}" -eq 0 ] || return 1
        arguments="$(cat "${CAPTURE_FILE}")"
        [[ "${arguments}" == *"-cluster=/var/lib/mongodb-automation/cluster-config.json"* ]] || return 1
        [[ "${arguments}" == *"-serveStatusPort=5000"* ]] || return 1
        [[ "${arguments}" == *"-ephemeralPortOffset=1"* ]] || return 1
        [[ "${arguments}" != *"-mmsBaseUrl="* ]] || return 1
        [[ "${arguments}" != *"-mmsApiKey="* ]] || return 1
        if [ -n "${architecture}" ]; then
            [[ "${arguments}" == *"-operatorMode=true"* ]] || return 1
            [[ "${arguments}" == *"-binariesFixedPath=${MMS_DOWNLOAD_BASE}/mongod/bin"* ]] || return 1
            [[ "${arguments}" != *"-useLocalMongoDbTools="* ]] || return 1
        else
            [[ "${arguments}" == *"-useLocalMongoDbTools=true"* ]] || return 1
            [[ "${arguments}" != *"-operatorMode="* ]] || return 1
        fi
    done
}
