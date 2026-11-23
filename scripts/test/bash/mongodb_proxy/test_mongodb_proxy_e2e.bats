#!/usr/bin/env bats

setup() {
    PROJECT_DIR="$(git rev-parse --show-toplevel)"
    export PROJECT_DIR
    cd "${PROJECT_DIR}"

    local variable
    for variable in DATABASE_REGISTRY OPERATOR_REGISTRY INIT_OPS_MANAGER_REGISTRY INIT_DATABASE_REGISTRY \
        MDB_AGENT_REGISTRY OPS_MANAGER_REGISTRY READINESS_PROBE_REGISTRY VERSION_UPGRADE_HOOK_REGISTRY \
        MONGODB_REPO_URL MDB_SEARCH_REPO_URL MDB_SEARCH_NAME MDB_SEARCH_VERSION MDB_ENVOY_IMAGE \
        MDB_SEARCH_METRICS_FORWARDER_IMAGE; do
        export "${variable}=registry.example/test"
    done
    for variable in OPERATOR_VERSION INIT_OPS_MANAGER_VERSION INIT_DATABASE_VERSION DATABASE_VERSION \
        AGENT_VERSION READINESS_PROBE_VERSION VERSION_UPGRADE_HOOK_VERSION; do
        export "${variable}=test-version"
    done
    export MDB_DEFAULT_ARCHITECTURE=static
    unset MDB_MONGODB_PROXY_IMAGE MDB_CUSTOM_AGENT_URL
}

@test "operator Helm values preserve chart proxy defaults without an image override" {
    run bash -c 'source scripts/funcs/operator_deployment; get_operator_helm_values'
    [ "${status}" -eq 0 ]
    [[ "${output}" != *"registry.mongodbProxy="* ]] || return 1
    [[ "${output}" != *"mongodbProxy.version="* ]] || return 1
}

@test "operator Helm values propagate the proxy image without changing legacy static or Agent selection" {
    export MDB_MONGODB_PROXY_IMAGE=registry.example:5000/team/mongodb-proxy:patch-test
    run bash -c 'source scripts/funcs/operator_deployment; get_operator_helm_values'
    [ "${status}" -eq 0 ]
    [[ "${output}" == *"registry.mongodbProxy=registry.example:5000/team"* ]] || return 1
    [[ "${output}" == *"mongodbProxy.name=mongodb-proxy"* ]] || return 1
    [[ "${output}" == *"mongodbProxy.version=patch-test"* ]] || return 1
    [[ "${output}" == *"operator.mdbDefaultArchitecture=static "* ]] || return 1
    [[ "${output}" == *"agent.version=test-version "* ]] || return 1
    [[ "${output}" == *"agent.customAgentUrl= "* ]] || return 1
}

@test "OpenShift rendering supplies the proxy image without an OLM related image" {
    export MDB_MONGODB_PROXY_IMAGE=registry.example:5000/team/mongodb-proxy:patch-test
    run bash -c '
        source scripts/funcs/operator_deployment
        read -ra helm_values <<< "$(get_operator_helm_values)"
        for value in "${helm_values[@]}"; do
            set -- "$@" --set "${value}"
        done
        helm template proxy-test helm_chart --values helm_chart/values-openshift.yaml \
            --show-only templates/operator.yaml "$@"
    '
    [ "${status}" -eq 0 ]
    [[ "${output}" == *"name: MDB_MONGODB_PROXY_IMAGE"* ]] || return 1
    [[ "${output}" != *"name: RELATED_IMAGE_MDB_MONGODB_PROXY_IMAGE_"* ]] || return 1
    [[ "${output}" == *'value: "registry.example:5000/team/mongodb-proxy:patch-test"'* ]] || return 1
}

load_patch_context() {
    export workdir="${BATS_TEST_TMPDIR}"
    echo proxy-test > "${workdir}/.namespace"
    export script_dir="${PROJECT_DIR}/scripts/dev/contexts"
    export BUILD_SCENARIO=patch version_id=proxy-patch RELEASE_VERSION=test-release
    export EVR_TASK_ID=proxy-test
    local variable
    for variable in mms_eng_test_aws_access_key mms_eng_test_aws_secret mms_eng_test_aws_region \
        e2e_cloud_qa_orgid_owner_ubi_cloudqa e2e_cloud_qa_apikey_owner_ubi_cloudqa \
        e2e_cloud_qa_user_owner_ubi_cloudqa community_private_preview_pullsecret_dockerconfigjson \
        cognito_user_pool_id cognito_workload_federation_client_id cognito_user_name \
        cognito_workload_federation_client_secret cognito_user_password cognito_workload_url cognito_workload_user_id; do
        export "${variable}="
    done
    unset REGISTRY OPERATOR_VERSION READINESS_PROBE_VERSION VERSION_UPGRADE_HOOK_VERSION
    source scripts/dev/contexts/evg-private-context
    echo "PROXY_IMAGE=${MDB_MONGODB_PROXY_IMAGE:-}"
}

@test "Evergreen context derives the proxy image from the patch registry and version" {
    run load_patch_context
    [ "${status}" -eq 0 ]
    [[ "${output}" == *"PROXY_IMAGE=268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-kubernetes-mongodb-proxy:proxy-patch"* ]] || return 1
}

@test "Evergreen context preserves an explicit proxy image" {
    export MDB_MONGODB_PROXY_IMAGE=registry.example/team/proxy:explicit
    run load_patch_context
    [ "${status}" -eq 0 ]
    [[ "${output}" == *"PROXY_IMAGE=${MDB_MONGODB_PROXY_IMAGE}"* ]] || return 1
}

@test "Evergreen supplies the proxy override before saving each context" {
    run python3 - <<'PY'
import yaml

with open(".evergreen-functions.yml") as source:
    functions = yaml.safe_load(source)["functions"]
for name in ("setup_context", "switch_context"):
    assert functions[name]["params"]["env"]["MDB_MONGODB_PROXY_IMAGE"] == "${mdb_mongodb_proxy_image}"
PY
    [ "${status}" -eq 0 ]
}

@test "test pod omits the proxy environment without an image override" {
    run helm template proxy-test scripts/evergreen/deployments/test-app \
        --show-only templates/mongodb-enterprise-tests.yaml
    [ "${status}" -eq 0 ]
    [[ "${output}" != *"name: MDB_MONGODB_PROXY_IMAGE"* ]] || return 1
}

@test "test pod receives the proxy image and retains legacy static" {
    run helm template proxy-test scripts/evergreen/deployments/test-app \
        --show-only templates/mongodb-enterprise-tests.yaml \
        --set mongodbProxyImage=registry.example/team/proxy:patch-test --set mdbDefaultArchitecture=static
    [ "${status}" -eq 0 ]
    [[ "${output}" == *"name: MDB_MONGODB_PROXY_IMAGE"* ]] || return 1
    [[ "${output}" == *'value: "registry.example/team/proxy:patch-test"'* ]] || return 1
    [[ "${output}" == *'value: "static"'* ]] || return 1
}

@test "proxy tests and OLM bundle preparation wait for required image builds" {
    run python3 - <<'PY'
import yaml

with open(".evergreen-tasks.yml") as source:
    tasks = yaml.safe_load(source)["tasks"]
for task_name in ("e2e_replica_set_mongodb_proxy", "e2e_om_appdb_mongodb_proxy"):
    proxy_test = next(task for task in tasks if task["name"] == task_name)
    dependencies = {(dep["variant"], dep["name"]) for dep in proxy_test.get("depends_on", [])}
    assert ("init_test_run", "build_mongodb_proxy_image") in dependencies, task_name
    assert ("init_test_run", "build_agent_images_ubi") in dependencies, task_name

with open(".evergreen.yml") as source:
    variants = yaml.safe_load(source)["buildvariants"]
olm = next(variant for variant in variants if variant["name"] == "init_tests_with_olm")
assert {"variant": "init_test_run", "name": "build_mongodb_proxy_image"} in olm["depends_on"]
PY
    [ "${status}" -eq 0 ]
}
