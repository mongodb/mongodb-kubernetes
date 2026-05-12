#!/usr/bin/env bash

set -Eeou pipefail

##
## The script deploys a single test application and waits until it finishes.
## All the Operator deployment, configuration and teardown work is done in 'e2e' script
##

source scripts/funcs/checks
source scripts/funcs/printing
source scripts/funcs/errors
source scripts/funcs/multicluster
source scripts/funcs/operator_deployment

check_env_var "TEST_NAME" "The 'TEST_NAME' must be specified to run the Operator single e2e test"


deploy_test_app() {
    printenv
    title "Deploying test application"
    local context=${1}
    local helm_template_file
    helm_template_file=$(mktemp)
    meko_tests_version="${OPERATOR_VERSION}"

    local arch
    arch=$(uname -m)

    case "${arch}" in
        aarch64|arm64)
            meko_tests_version="${meko_tests_version}-arm64"
            ;;
        ppc64le)
            meko_tests_version="${meko_tests_version}-ppc64le"
            ;;
        s390x)
            meko_tests_version="${meko_tests_version}-s390x"
            ;;
        *)
            echo "amd64 host, using default meko_tests_version"
            ;;
    esac

    IS_PATCH="${IS_PATCH:-default_patch}"
    TASK_NAME="${TASK_NAME:-default_task}"
    EXECUTION="${EXECUTION:-default_execution}"
    BUILD_ID="${BUILD_ID:-default_build_id}"
    BUILD_VARIANT="${BUILD_VARIANT:-default_build_variant}"

    chart_info=$(scripts/dev/run_python.sh scripts/release/oci_chart_info.py --build-scenario "${BUILD_SCENARIO}") || { echo "Failed to generate chart_info" ; exit 1; }

    helm_oci_repository=$(echo "${chart_info}" | jq -r '.repository') || { echo "Failed to parse repository from chart_info"; exit 1; }
    helm_oci_registry="${helm_oci_repository%%/*}"
    helm_oci_version_prefix=$(echo "${chart_info}" | jq -r '.version_prefix // empty') || { echo "Failed to parse version_prefix from chart_info"; exit 1; }
    helm_oci_version="${helm_oci_version_prefix:-}${OPERATOR_VERSION}"

    # note, that the 4 last parameters are used only for Mongodb resource testing - not for Ops Manager
    helm_params=(
        "--set" "taskId=${task_id:-'not-specified'}"
        "--set" "namespace=${NAMESPACE}"
        "--set" "taskName=${task_name}"
        "--set" "mekoTestsRegistry=${MEKO_TESTS_REGISTRY}"
        "--set" "mekoTestsVersion=${meko_tests_version}"
        "--set" "versionId=${VERSION_ID}"
        "--set" "aws.accessKey=${AWS_ACCESS_KEY_ID}"
        "--set" "aws.secretAccessKey=${AWS_SECRET_ACCESS_KEY}"
        "--set" "skipExecution=${SKIP_EXECUTION:-'false'}"
        "--set" "baseUrl=${OM_BASE_URL:-http://ops-manager-svc.${OPS_MANAGER_NAMESPACE}.svc.cluster.local:8080}"
        "--set" "apiKey=${OM_API_KEY:-}"
        "--set" "apiUser=${OM_USER:-admin}"
        "--set" "orgId=${OM_ORGID:-}"
        "--set" "imagePullSecrets=image-registries-secret"
        "--set" "managedSecurityContext=${MANAGED_SECURITY_CONTEXT:-false}"
        "--set" "registry=${REGISTRY}"
        "--set" "mdbDefaultArchitecture=${MDB_DEFAULT_ARCHITECTURE:-'non-static'}"
        "--set" "clusterDomain=${CLUSTER_DOMAIN:-'cluster.local'}"
        "--set" "cognito_user_pool_id=${cognito_user_pool_id}"
        "--set" "cognito_workload_federation_client_id=${cognito_workload_federation_client_id}"
        "--set" "cognito_user_name=${cognito_user_name}"
        "--set" "cognito_workload_federation_client_secret=${cognito_workload_federation_client_secret}"
        "--set" "cognito_user_password=${cognito_user_password}"
        "--set" "cognito_workload_url=${cognito_workload_url}"
        "--set" "cognito_workload_user_id=${cognito_workload_user_id}"
        "--set" "helm.oci.version=${helm_oci_version}"
        "--set" "helm.oci.registry=${helm_oci_registry}"
        "--set" "helm.oci.repository=${helm_oci_repository}"
        "--set" "autoEmbedding.providerMongoDB.indexingKey=${AI_MONGODB_EMBEDDING_INDEXING_KEY}"
        "--set" "autoEmbedding.providerMongoDB.queryKey=${AI_MONGODB_EMBEDDING_QUERY_KEY}"
    )

    # shellcheck disable=SC2154
    if [[ ${KUBE_ENVIRONMENT_NAME} = "multi" ]]; then
        helm_params+=("--set" "multiCluster.memberClusters=${MEMBER_CLUSTERS}")
        helm_params+=("--set" "multiCluster.centralCluster=${CENTRAL_CLUSTER}")
        helm_params+=("--set" "multiCluster.testPodCluster=${test_pod_cluster}")
    fi

    if [[ -n "${CUSTOM_OM_VERSION:-}" ]]; then
        # The test needs to create an OM resource with specific version
        helm_params+=("--set" "customOmVersion=${CUSTOM_OM_VERSION}")
    fi
    if [[ -n "${pytest_addopts:-}" ]]; then
        # The test needs to create an OM resource with specific version
        helm_params+=("--set" "pytest.addopts=${pytest_addopts:-}")
    fi
    # As soon as we are having one OTEL expansion it means we want to trace and send everything to our trace provider.
    # otel_parent_id is a special case (hence lower cased) since it is directly coming from evergreen and not via our
    # make switch mechanism. We need the "freshest" parent_id otherwise we are attaching to the wrong parent span.
    # PYTEST_OTEL_ENABLED is set to false on s390x (via root-context) where pytest-opentelemetry is unavailable.
    helm_params+=("--set" "pytestOtelEnabled=${PYTEST_OTEL_ENABLED:-true}")

    if [[ -n "${otel_parent_id:-}" ]]; then
        otel_resource_attributes="evergreen.version.id=${VERSION_ID:-},evergreen.version.requester=${requester:-},mck.git_branch=${branch_name:-},evergreen.version.pr_num=${github_pr_number:-},mck.git_commit=${github_commit:-},mck.revision=${revision:-},is_patch=${IS_PATCH},evergreen.task.name=${TASK_NAME},evergreen.task.execution=${EXECUTION},evergreen.build.id=${BUILD_ID},evergreen.build.name=${BUILD_VARIANT},evergreen.task.id=${task_id},evergreen.project.id=${project_identifier:-}"
        # shellcheck disable=SC2001
        escaped_otel_resource_attributes=$(echo "${otel_resource_attributes}" | sed 's/,/\\,/g')
        # The test needs to create an OM resource with specific version
        helm_params+=("--set" "otel_parent_id=${otel_parent_id:-"unknown"}")
        helm_params+=("--set" "otel_trace_id=${OTEL_TRACE_ID:-"unknown"}")
        helm_params+=("--set" "otel_endpoint=${OTEL_COLLECTOR_ENDPOINT:-"unknown"}")
        helm_params+=("--set" "otel_resource_attributes=${escaped_otel_resource_attributes}")
    fi
    if [[ -n "${CUSTOM_OM_PREV_VERSION:-}" ]]; then
        # The test needs to create an OM resource with specific version
        helm_params+=("--set" "customOmPrevVersion=${CUSTOM_OM_PREV_VERSION}")
    fi
    if [[ -n "${PERF_TASK_DEPLOYMENTS:-}" ]]; then
        # The test needs to create an OM resource with specific version
        helm_params+=("--set" "taskDeployments=${PERF_TASK_DEPLOYMENTS}")
    fi
    if [[ -n "${PERF_TASK_REPLICAS:-}" ]]; then
        # The test needs to create an OM resource with specific version
        helm_params+=("--set" "taskReplicas=${PERF_TASK_REPLICAS}")
    fi
    if [[ -n "${CUSTOM_MDB_VERSION:-}" ]]; then
        # The test needs to test MongoDB of a specific version
        helm_params+=("--set" "customOmMdbVersion=${CUSTOM_MDB_VERSION}")
    fi
    if [[ -n "${CUSTOM_MDB_PREV_VERSION:-}" ]]; then
        # The test needs to test MongoDB of a previous version
        helm_params+=("--set" "customOmMdbPrevVersion=${CUSTOM_MDB_PREV_VERSION}")
    fi
    if [[ -n "${CUSTOM_APPDB_VERSION:-}" ]]; then
        helm_params+=("--set" "customAppDbVersion=${CUSTOM_APPDB_VERSION}")
    fi

    if [[ -n "${PROJECT_DIR:-}" ]]; then
        helm_params+=("--set" "projectDir=/mongodb-kubernetes")
    fi

    if [[ "${LOCAL_OPERATOR}" == true ]]; then
        helm_params+=("--set" "localOperator=true")
    fi

    if [[ "${OM_DEBUG_HTTP}" == "true" ]]; then
        helm_params+=("--set" "omDebugHttp=true")
    fi

    helm_params+=("--set" "opsManagerVersion=${ops_manager_version}")

    helm template "scripts/evergreen/deployments/test-app" "${helm_params[@]}" > "${helm_template_file}" || exit 1

    cat "${helm_template_file}"

    kubectl --context "${context}" -n "${NAMESPACE}" delete -f "${helm_template_file}" 2>/dev/null  || true

    kubectl --context "${context}" -n "${NAMESPACE}" apply -f "${helm_template_file}"
}

wait_until_pod_is_running_or_failed_or_succeeded() {
    local context=${1}
    # Do wait while the Pod is not yet running or failed (can be in Pending or ContainerCreating state)
    # Note that the pod may jump to Failed/Completed state quickly - so we need to give up waiting on this as well
    echo "Waiting until the test application gets to Running state..."

    is_running_cmd="kubectl --context '${context}' -n ${NAMESPACE} get pod ${TEST_APP_PODNAME} -o jsonpath={.status.phase} | grep -q 'Running'"

    # test app usually starts instantly but sometimes (quite rarely though) may require more than a min to start
    # in Evergreen so let's wait for 2m
    timeout --foreground "2m" bash -c "while ! ${is_running_cmd}; do printf .; sleep 1; done;"
    echo

    if ! eval "${is_running_cmd}"; then
        error "Test application failed to start on time!"
        kubectl --context "${context}" -n "${NAMESPACE}"  describe pod "${TEST_APP_PODNAME}"
        fatal "Failed to run test application - exiting"
    fi
}

test_app_ended() {
    local context="${1}"
    local status
    status="$(kubectl --context "${context}" get pod mongodb-enterprise-operator-tests -n "${NAMESPACE}" -o jsonpath="{.status}" | jq -r '.containerStatuses[] | select(.name == "mongodb-enterprise-operator-tests")'.state.terminated.reason)"
    [[ "${status}" = "Error" || "${status}" = "Completed" ]]
}

# Run the e2e test marker inside the project's devcontainer instead of deploying
# the test-app pod. Used when TEST_RUN_MODE=local.
#
# Flow:
#   1. Install @devcontainers/cli on the EVG host if missing.
#   2. Write an EVG-specific .devcontainer/compose.user.yml that:
#        - strips personal-home bind mounts that don't exist on the EVG task host
#        - stubs out evg-host-proxy / gost-proxy (only useful when the devcontainer
#          tunnels to a remote EVG host; here kind runs on the same host)
#        - overrides k8s-proxy to use the published image instead of the sibling
#          kube-forwarding-proxy clone (which only exists on a developer laptop)
#        - joins devcontainer + k8s-proxy to the kind docker network so they can
#          reach the kind apiserver at kind-control-plane:6443 directly
#        - pulls EVG expansions in via env_file=/tmp/devcontainer.evg.env
#   3. Run .devcontainer/scripts/initialize.sh (handles compose.generated.yml).
#   4. `devcontainer up`, capture containerId from the JSON line for `docker exec`.
#   5. Translate kind kubeconfig (127.0.0.1:<random> → kind-control-plane:6443),
#      register with k8s-proxy, drop at ~/.kube/config inside the container so
#      conftest.py's KubeConfigMerger picks it up.
#   6. `docker exec` pytest with the pod-equivalent env (BUILD_SCENARIO,
#      OPERATOR_VERSION, etc.) and the helm-chart info resolved inside the
#      container (host has different PROJECT_DIR after `make switch` on EVG).
#   7. EXIT trap: dump every compose service's logs to logs/local-runner/ and
#      tear the stack down.
run_tests_locally() {
    local task_name=${1}
    title "Running e2e test ${task_name} inside the devcontainer (TEST_RUN_MODE=local)"

    mkdir -p logs/
    local repo_root
    repo_root="$(pwd)"

    # Promote to script-level globals so the EXIT trap (which fires after this
    # function has returned and shell-cleanup runs) still has the values.
    # Without this, ${repo_root} expands to empty and `mkdir -p /logs/...`
    # fails with permission denied.
    EVG_DEVCONTAINER_REPO_ROOT="${repo_root}"
    EVG_DEVCONTAINER_COMPOSE_PROJECT=""

    cleanup_devcontainer_runner() {
        local exit_rc=$?
        title "Collecting devcontainer + sidecar logs"
        local _root="${EVG_DEVCONTAINER_REPO_ROOT:-$(pwd)}"
        local _project="${EVG_DEVCONTAINER_COMPOSE_PROJECT:-}"
        mkdir -p "${_root}/logs/local-runner"
        if [[ -n "${_project}" ]]; then
            docker compose -p "${_project}" ps -a \
                > "${_root}/logs/local-runner/_ps.txt" 2>&1 || true
            local svc
            for svc in $(docker compose -p "${_project}" ps -a --services 2>/dev/null); do
                docker compose -p "${_project}" logs --no-color --timestamps "${svc}" \
                    > "${_root}/logs/local-runner/${svc}.log" 2>&1 || true
            done
            docker compose -p "${_project}" down --remove-orphans 2>/dev/null || true
        fi
        return ${exit_rc}
    }
    trap cleanup_devcontainer_runner EXIT

    if ! command -v devcontainer >/dev/null 2>&1; then
        title "Installing @devcontainers/cli"
        if ! command -v npm >/dev/null 2>&1; then
            sudo apt-get update -qq
            sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nodejs npm
        fi
        sudo npm install -g @devcontainers/cli
        # `npm install -g` may install into a prefix that's not on the EVG agent's
        # PATH (e.g. /usr vs /usr/local depending on how npm was installed).
        local npm_prefix
        npm_prefix=$(npm config get prefix 2>/dev/null || echo "")
        echo "npm config get prefix: ${npm_prefix}"
        if [[ -n "${npm_prefix}" && -d "${npm_prefix}/bin" ]]; then
            export PATH="${npm_prefix}/bin:${PATH}"
        fi
        hash -r
        if ! command -v devcontainer >/dev/null 2>&1; then
            echo "ERROR: devcontainer CLI not on PATH after install. Diagnostics:"
            echo "  which node:  $(command -v node || echo none)"
            echo "  which npm:   $(command -v npm || echo none)"
            echo "  npm root -g: $(npm root -g 2>/dev/null || echo unknown)"
            sudo find / -name devcontainer -type f -executable 2>/dev/null | head -5 | sed 's/^/  found: /'
            return 127
        fi
        echo "devcontainer CLI: $(command -v devcontainer)"
    fi

    title "Writing .devcontainer/compose.user.yml overrides for EVG"
    # Override semantics:
    #   - !override on volumes / environment replaces the whole list from compose.yml.
    #   - networks is merged additively, so adding `kind` keeps the existing
    #     `devcontainer` entry.
    cat > .devcontainer/compose.user.yml <<'YAML'
services:
  devcontainer:
    # Replace personal home-dir bind mounts (~/.evergreen.yml, ~/.kanopy,
    # ~/.claude) and the ssh-auth-sock volume; none exist on the EVG task host.
    volumes: !override
      - ..:/workspace:cached
      - venv:/workspace/venv
      - bin:/workspace/bin
      - gomodcache:/go/pkg/mod
      - gocache:/home/vscode/.cache/go-build
      - uvcache:/home/vscode/.cache/uv
      - helmcache:/home/vscode/.cache/helm
    extra_hosts:
      - "host.docker.internal:host-gateway"
    # Pipe the EVG expansion env vars (mms_eng_test_aws_*, cognito_*, …) into the
    # container so private-context references resolve. Generated by single_e2e.sh
    # before `devcontainer up`.
    env_file:
      - /tmp/devcontainer.evg.env
    # Also attach to kind's docker network so the devcontainer can reach the kind
    # apiserver at kind-control-plane:6443 via Docker DNS. Kind binds the
    # apiserver to 127.0.0.1 on the host; that's unreachable from a sibling
    # container directly, but kind's control-plane container is on the kind
    # network and its TLS cert SAN includes "kind-control-plane".
    networks:
      devcontainer: {}
      kind: {}

  # SSH-tunnel sidecars are pointless on EVG — kind runs on the same host. Replace
  # them with idle stubs so compose still satisfies depends_on chains.
  evg-host-proxy:
    image: alpine:3.20
    entrypoint: ["/bin/sh", "-c"]
    command: ["echo 'evg-host-proxy stubbed on EVG'; sleep infinity"]
    restart: "no"
    volumes: !override []
    environment: !override []

  gost-proxy:
    image: alpine:3.20
    entrypoint: ["/bin/sh", "-c"]
    command: ["echo 'gost-proxy stubbed on EVG'; sleep infinity"]
    restart: "no"
    depends_on: !override []
    ports: !override []

  # Pin k8s-proxy to a published image (no sibling kube-forwarding-proxy clone
  # exists on the EVG task host). The per-context-shutdown fix that motivated
  # the local build is only needed for concurrent worktree teardown on macOS,
  # not on a single-stack EVG run.
  k8s-proxy:
    build: !reset null
    image: ghcr.io/fealebenpae/kube-forwarding-proxy:0.8.0
    extra_hosts:
      - "host.docker.internal:host-gateway"
    networks:
      devcontainer:
        ipv4_address: 172.16.0.10
      kind: {}

networks:
  kind:
    external: true
YAML

    # private-context is normally set up by the EVG `setup_context` function
    # (which copies evg-private-context). Belt-and-suspenders here: ensure it
    # exists before devcontainer's on-create scripts run.
    if [[ ! -f scripts/dev/contexts/private-context ]]; then
        echo "Copying evg-private-context → private-context"
        cp scripts/dev/contexts/evg-private-context scripts/dev/contexts/private-context
    fi
    # evg-private-context hardcodes GOROOT=/opt/golang/go1.25 (the EVG host's
    # Go install). Inside the devcontainer the Go feature installs Go elsewhere
    # (e.g. /usr/local/go), and root-context's `go env GOROOT` call against the
    # bogus path errors out with `go: cannot find GOROOT directory:
    # /opt/golang/go1.25`. Comment out the host-specific GOROOT export and the
    # PATH addition that depends on it; let root-context auto-detect via go env.
    sed -i.bak \
        -e 's|^export GOROOT=.*$|# GOROOT auto-detected from devcontainer (TEST_RUN_MODE=local)|' \
        -e '/\${GOROOT}\/bin/,/^fi$/ s|^|# |' \
        scripts/dev/contexts/private-context
    rm -f scripts/dev/contexts/private-context.bak

    # evg-private-context references EVG expansion vars (mms_eng_test_aws_*,
    # cognito_*, e2e_cloud_qa_*, task_name, build_id, …). Those live in the EVG
    # task shell's env via include_expansions_in_env, but `devcontainer up`
    # spawns a fresh process tree inside docker, so they won't reach the
    # container without help. Dump them to a file and feed it via env_file.
    local devcontainer_env_file="/tmp/devcontainer.evg.env"
    title "Exporting EVG expansion env vars to ${devcontainer_env_file}"
    # Whitelist matches e2e_include_expansions_in_env in .evergreen-functions.yml,
    # plus task-shell vars that evg-private-context relies on.
    local evg_env_vars=(
        cognito_user_pool_id cognito_workload_federation_client_id cognito_user_name
        cognito_workload_federation_client_secret cognito_user_password
        cognito_workload_url cognito_workload_user_id
        ARTIFACTORY_PASSWORD ARTIFACTORY_USERNAME GRS_PASSWORD GRS_USERNAME
        OVERRIDE_VERSION_ID PKCS11_URI
        branch_name build_id build_variant distro
        e2e_cloud_qa_apikey_owner_ubi_cloudqa e2e_cloud_qa_orgid_owner_ubi_cloudqa
        e2e_cloud_qa_user_owner_ubi_cloudqa
        ecr_registry ecr_registry_needs_auth execution
        github_commit github_pr_head_branch image_name is_patch
        mms_eng_test_aws_access_key mms_eng_test_aws_region mms_eng_test_aws_secret
        openshift_token openshift_url
        otel_collector_endpoint otel_parent_id otel_trace_id
        registry requester task_name triggered_by_git_tag version_id workdir
        community_private_preview_pullsecret_dockerconfigjson
        RELEASE_INITIAL_VERSION RELEASE_INITIAL_COMMIT_SHA
        OPERATOR_VERSION READINESS_PROBE_VERSION VERSION_UPGRADE_HOOK_VERSION
        BUILD_SCENARIO MDB_BASH_DEBUG
        AI_MONGODB_EMBEDDING_INDEXING_KEY AI_MONGODB_EMBEDDING_QUERY_KEY
        EVR_TASK_ID KUBE_ENVIRONMENT_NAME NAMESPACE TEST_RUN_MODE
        TASK_NAME REGISTRY VERSION_ID
    )
    : > "${devcontainer_env_file}"
    local var
    for var in "${evg_env_vars[@]}"; do
        if [[ "${var}" == "workdir" ]]; then
            # Inside the container the bind-mounted workspace is /workspace; the
            # host's workdir path (/data/mci/<task>/) doesn't exist. Without
            # this override, evg-private-context's "${workdir}/.namespace"
            # lookup fails and the on-create chain aborts.
            printf 'workdir=/workspace\n' >> "${devcontainer_env_file}"
        else
            printf '%s=%s\n' "${var}" "${!var:-}" >> "${devcontainer_env_file}"
        fi
    done
    echo "wrote ${devcontainer_env_file} ($(wc -l <"${devcontainer_env_file}") vars)"

    # The host's $workdir holds .namespace (written by setup_cloud_qa) and
    # .ops-manager-env (written when there's an OM project). The container's
    # workdir override above points at /workspace, so mirror those files there
    # so evg-private-context picks up the same values the host already used.
    local host_workdir="${workdir:-${repo_root}/..}"
    if [[ -f "${host_workdir}/.namespace" ]]; then
        cp "${host_workdir}/.namespace" "${repo_root}/.namespace"
        echo "copied host .namespace ($(cat "${repo_root}/.namespace")) to ${repo_root}/.namespace"
    fi
    if [[ -f "${host_workdir}/.ops-manager-env" ]]; then
        cp "${host_workdir}/.ops-manager-env" "${repo_root}/.ops-manager-env"
    fi

    title "Running .devcontainer initialize.sh on EVG host"
    # ssh-agent.sh auto-skips when SSH_AUTH_SOCK is empty (EVG case); evergreen-cli.sh
    # resolves the linux CLI URL via the host's evergreen binary; the others touch
    # compose.generated.yml / compose.user.yml only.
    bash .devcontainer/scripts/initialize.sh || true

    title "devcontainer up"
    local up_log
    up_log=$(mktemp)
    devcontainer up \
        --workspace-folder "${repo_root}" \
        --remove-existing-container \
        2>&1 | tee "${up_log}"

    # `devcontainer up` prints a JSON line at the end carrying containerId +
    # composeProjectName. Use them for subsequent `docker exec`/`docker cp` —
    # `devcontainer exec` in v15 sometimes loses track of the container.
    local devcontainer_id devcontainer_compose_project
    devcontainer_id=$(grep -oE '"containerId":"[a-f0-9]+"' "${up_log}" | tail -1 | cut -d'"' -f4)
    devcontainer_compose_project=$(grep -oE '"composeProjectName":"[^"]+"' "${up_log}" | tail -1 | cut -d'"' -f4)
    rm -f "${up_log}"
    if [[ -z "${devcontainer_id}" ]]; then
        echo "ERROR: failed to extract containerId from devcontainer up output"
        return 1
    fi
    echo "devcontainer container id:    ${devcontainer_id}"
    echo "devcontainer compose project: ${devcontainer_compose_project}"
    EVG_DEVCONTAINER_COMPOSE_PROJECT="${devcontainer_compose_project}"

    title "Translating kind kubeconfig for in-cluster-network access"
    local src_kubeconfig translated_kubeconfig kind_control_plane
    src_kubeconfig="${KUBECONFIG:-${HOME}/.kube/config}"
    translated_kubeconfig=$(mktemp)
    # devcontainer joined the `kind` docker network → can reach the apiserver
    # directly at the control-plane container's hostname on port 6443. TLS cert
    # SAN includes "kind-control-plane" so verification succeeds.
    kind_control_plane=$(docker ps \
        --filter "label=io.x-k8s.kind.role=control-plane" \
        --format '{{.Names}}' | head -1)
    if [[ -z "${kind_control_plane}" ]]; then
        kind_control_plane="kind-control-plane"
    fi
    echo "kind control-plane container: ${kind_control_plane}"
    sed -E "s|https://127\.0\.0\.1:[0-9]+|https://${kind_control_plane}:6443|g" \
        "${src_kubeconfig}" > "${translated_kubeconfig}"

    title "Registering translated kubeconfig with k8s-proxy"
    docker exec -i "${devcontainer_id}" \
        bash -c 'curl -fsS -X PATCH --data-binary @- "http://${K8S_FWD_PROXY}/kubeconfig"' \
        < "${translated_kubeconfig}" || echo "WARNING: kubeconfig PATCH failed"

    docker cp "${translated_kubeconfig}" "${devcontainer_id}:/workspace/.generated/evg-host.kubeconfig"
    docker exec "${devcontainer_id}" sudo chown vscode:vscode /workspace/.generated/evg-host.kubeconfig

    # tests/conftest.py uses KUBE_CONFIG_DEFAULT_LOCATION (~/.kube/config)
    # directly via KubeConfigMerger; it ignores the KUBECONFIG env var. Drop the
    # translated kubeconfig at the default path so the merger picks it up.
    docker exec "${devcontainer_id}" sudo install -d -o vscode -g vscode /home/vscode/.kube
    docker cp "${translated_kubeconfig}" "${devcontainer_id}:/home/vscode/.kube/config"
    docker exec "${devcontainer_id}" sudo chown vscode:vscode /home/vscode/.kube/config

    title "Running pytest inside devcontainer"
    # Derive DEFAULT_HELM_CHART_PATH / OCI_HELM_* inside the container — running
    # oci_chart_info.py via run_python.sh on the host fails because
    # .generated/context.export.env carries container paths (PROJECT_DIR=/workspace)
    # written by `make switch` during on-create.
    set +e
    docker exec \
        -e KUBECONFIG=/workspace/.generated/evg-host.kubeconfig \
        -e BUILD_SCENARIO="${BUILD_SCENARIO}" \
        -e OPERATOR_VERSION="${OPERATOR_VERSION}" \
        -e NAMESPACE="${NAMESPACE}" \
        -e VERSION_ID="${VERSION_ID}" \
        -e REGISTRY="${REGISTRY}" \
        -e MDB_DEFAULT_ARCHITECTURE="${MDB_DEFAULT_ARCHITECTURE:-non-static}" \
        -e CLUSTER_DOMAIN="${CLUSTER_DOMAIN:-cluster.local}" \
        -e PYTEST_RUN_NAME="${task_name}" \
        -e PYTHONUNBUFFERED=true \
        -e PYTHONWARNINGS="ignore:yaml.YAMLLoadWarning,ignore:urllib3.InsecureRequestWarning" \
        -u vscode \
        "${devcontainer_id}" \
        bash -c "
            set -Eeou pipefail
            cd /workspace
            # Load .generated/context.export.env so AWS_ACCESS_KEY_ID /
            # AWS_SECRET_ACCESS_KEY (mapped from mms_eng_test_aws_* in
            # evg-private-context) are available to the helm-ECR login fixture.
            source scripts/dev/set_env_context.sh
            if [[ ! -x venv/bin/pytest ]]; then
                scripts/dev/recreate_python_venv.sh
            fi
            source venv/bin/activate

            chart_info=\$(scripts/dev/run_python.sh scripts/release/oci_chart_info.py --build-scenario \"\${BUILD_SCENARIO}\")
            export OCI_HELM_REGISTRY=\$(echo \"\${chart_info}\" | jq -r .registry)
            export OCI_HELM_REPOSITORY=\$(echo \"\${chart_info}\" | jq -r .repository)
            export OCI_HELM_REGION=\$(echo \"\${chart_info}\" | jq -r .region)
            helm_oci_version_prefix=\$(echo \"\${chart_info}\" | jq -r '.version_prefix // empty')
            export OCI_HELM_VERSION=\"\${helm_oci_version_prefix}\${OPERATOR_VERSION}\"
            export DEFAULT_HELM_CHART_PATH=\"oci://\${OCI_HELM_REGISTRY}/\${OCI_HELM_REPOSITORY}/mongodb-kubernetes\"
            echo \"DEFAULT_HELM_CHART_PATH=\${DEFAULT_HELM_CHART_PATH}\"
            echo \"OCI_HELM_VERSION=\${OCI_HELM_VERSION}\"

            cd docker/mongodb-kubernetes-tests
            pytest -vv -m '${task_name}' --junitxml=/workspace/logs/myreport.xml
        " 2>&1 | tee "${repo_root}/logs/test_app.log"
    local rc=${PIPESTATUS[0]}
    set -e

    return ${rc}
}

# Will run the test application and wait for its completion.
run_tests() {
    local task_name=${1}
    local operator_context
    local test_pod_context
    operator_context="$(kubectl config current-context)"

    test_pod_context="${operator_context}"
    if [[ "${KUBE_ENVIRONMENT_NAME}" = "multi" ]]; then
        operator_context="${CENTRAL_CLUSTER}"
        # shellcheck disable=SC2154,SC2269
        test_pod_context="${test_pod_cluster:-${operator_context}}"
    fi

    if [[ "${TEST_RUN_MODE:-pod}" == "local" ]]; then
        # Local mode still needs operator-installation-config in NAMESPACE —
        # fixtures (operator_installation_config) read it at module setup.
        # configure_multi_cluster_environment is required for multi just like
        # in pod mode.
        if [[ "${KUBE_ENVIRONMENT_NAME}" = "multi" ]]; then
            configure_multi_cluster_environment
        fi
        prepare_operator_config_map "${operator_context}"
        run_tests_locally "${task_name}"
        return
    fi

    echo "Operator running in cluster ${operator_context}"
    echo "Test pod running in cluster ${test_pod_context}"

    TEST_APP_PODNAME=mongodb-enterprise-operator-tests

    if [[ "${KUBE_ENVIRONMENT_NAME}" = "multi" ]]; then
        configure_multi_cluster_environment
    fi

    prepare_operator_config_map "${operator_context}"

    deploy_test_app "${test_pod_context}"

    wait_until_pod_is_running_or_failed_or_succeeded "${test_pod_context}"

    title "Running e2e test ${task_name}"

    # we don't output logs to file when running tests locally
    if [[ "${MODE-}" == "dev" ]]; then
        kubectl --context "${test_pod_context}" -n "${NAMESPACE}" logs "${TEST_APP_PODNAME}" -c mongodb-enterprise-operator-tests -f
    else
        output_filename="logs/test_app.log"

        # At this time ${TEST_APP_PODNAME} has finished running, so we don't follow (-f) it
        # Similarly, the operator deployment has finished with our tests, so we print whatever we have
        # until this moment and go continue with our lives
        kubectl --context "${test_pod_context}" -n "${NAMESPACE}" logs "${TEST_APP_PODNAME}" -c mongodb-enterprise-operator-tests -f | tee "${output_filename}" || true
    fi


    # Waiting a bit until the pod gets to some end
    while ! test_app_ended "${test_pod_context}"; do printf .; sleep 1; done;
    echo

    # We need to make sure to access this file after the test has finished
    kubectl --context "${test_pod_context}" -n "${NAMESPACE}" -c keepalive cp "${TEST_APP_PODNAME}":/tmp/results/myreport.xml logs/myreport.xml
    kubectl --context "${test_pod_context}" -n "${NAMESPACE}" -c keepalive cp "${TEST_APP_PODNAME}":/tmp/results/pytest-debug.log logs/pytest-debug.log 2>/dev/null || true
    kubectl --context "${test_pod_context}" -n "${NAMESPACE}" -c keepalive cp "${TEST_APP_PODNAME}":/tmp/diagnostics logs

    status="$(kubectl --context "${test_pod_context}" get pod "${TEST_APP_PODNAME}" -n "${NAMESPACE}" -o jsonpath="{ .status }" | jq -r '.containerStatuses[] | select(.name == "mongodb-enterprise-operator-tests")'.state.terminated.reason)"
    [[ "${status}" == "Completed" ]]
}

mkdir -p logs/

TESTS_OK=0
# shellcheck disable=SC2153
run_tests "${TEST_NAME}" || TESTS_OK=1

echo "Tests have finished with the following exit code: ${TESTS_OK}"

[[ "${TESTS_OK}" -eq 0 ]]
