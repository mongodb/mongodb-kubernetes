#!/usr/bin/env bash

set -Eeou pipefail

## We need to make sure this script does not fail if one of
## the kubectl commands fails.
set +e

source scripts/funcs/printing

_dump_all_non_default_namespaces() {
    local context="${1}"
    local original_context
    original_context="$(kubectl config current-context)"
    prefix="${1:-${original_context}}_"
    # shellcheck disable=SC2154
    if [[ "${KUBE_ENVIRONMENT_NAME:-}" != "multi" ]]; then
        prefix=""
    fi

    mkdir -p logs
    namespaces=$(kubectl --context="${context}" get namespace --output=jsonpath="{.items[*].metadata.name}" | tr ' ' '\n' | \
      grep -v "default" | \
      grep -v "kube-node-lease" | \
      grep -v "kube-node-lease" | \
      grep -v "kube-public" | \
      grep -v "kube-system" | \
      grep -v "local-path-storage" | \
      grep -v "gmp-" | \
      grep -v "gke-managed" | \
      grep -v "local-path-storage" | \
      grep -v "local-path-storage" | \
      grep -v "metallb-system"
    )

    for ns in ${namespaces}; do
        if kubectl --context="${context}" get namespace "${ns}" -o jsonpath='{.metadata.annotations}'; then
            echo "Dumping all diagnostic information for namespace ${ns}"
            dump_namespace "${context}" "${ns}" "${prefix}"
        fi
    done
}

dump_all_non_default_namespaces() {
  local context="${1}"
  _dump_all_non_default_namespaces "$@" 2>&1 | prepend "${context}"
}

_dump_all() {
    [[ "${MODE-}" = "dev" ]] && return

    mkdir -p logs

    local context="${1}"
    prefix="${context}_"
    if [[ "${KUBE_ENVIRONMENT_NAME:-}" != "multi" ]]; then
        prefix=""
    fi

    # The dump process usually happens for a single namespace (the one the test and the operator are installed to)
    # but in some exceptional cases (e.g. clusterwide operator) there can be more than 1 namespace to print diagnostics
    # In this case the python test app may create the test namespace and add necessary labels and annotations so they
    # would be dumped for diagnostics as well
    for ns in $(kubectl --context="${context}" get namespace -l "evg=task" --output=jsonpath={.items..metadata.name}); do
        if kubectl --context="${context}" get namespace "${ns}" -o jsonpath='{.metadata.annotations}' | grep -q "${task_id:-'not-specified'}"; then
            echo "Dumping all diagnostic information for namespace ${ns}"
            dump_namespace "${context}" "${ns}" "${prefix}"
        fi
    done

    if kubectl --context="${context}" get namespace "olm" &>/dev/null; then
      echo "Dumping olm namespace"
      dump_namespace "${context}" "olm" "olm"
    fi

    kubectl --context="${context}" -n "kube-system" get configmap coredns -o yaml > "logs/${prefix}coredns.yaml"

    kubectl --context="${context}" events --all-namespaces > "logs/${prefix}kube_events.json"
}

dump_all() {
  local context="${1}"
  _dump_all "$@" 2>&1 | prepend "${context}"
}

dump_objects() {
    local context=$1
    local object=$2
    local msg=$3
    local namespace=${4}
    local action=${5:-get -o yaml}
    local out_file=${6:-""}

    # First check if the resource type exists
    if ! kubectl --context="${context}" get "${object}" --no-headers -o name -n "${namespace}" >/dev/null 2>&1; then
        # Resource type doesn't exist, skip silently
        return
    fi

    # Check if there are any objects of this type
    local resource_count
    resource_count=$(kubectl --context="${context}" get "${object}" --no-headers -o name -n "${namespace}" 2>/dev/null | wc -l)
    if [ "${resource_count}" -eq 0 ]; then
        # Resource type exists but no objects of this type, return
        return
    fi

    # Capture output first to check if it contains actual resources
    local temp_output
    # shellcheck disable=SC2086
    temp_output=$(kubectl --context="${context}" -n "${namespace}" ${action} "${object}" 2>&1)

    # Check if output contains actual resources (not just empty list)
    # Skip if it's an empty YAML list (contains "items: []")
    if printf '%s\n' "${temp_output}" | grep -Fq "items: []"; then
        # Empty list, don't create file
        return
    fi

    if [[ -n "${out_file}" ]]; then
      {
        header "${msg}"
        echo "${temp_output}"
      } > "${out_file}"
    else
      header "${msg}"
      # shellcheck disable=SC2086
      kubectl --context="${context}" -n "${namespace}" ${action} "${object}" 2>&1
    fi
}

# get_operator_managed_pods returns a list of names of the Pods that are managed
# by the Operator.
get_operator_managed_pods() {
    local context=${1}
    local namespace=${2}
    kubectl --context="${context}" get pods --namespace "${namespace}" --selector=controller=mongodb-enterprise-operator --no-headers -o custom-columns=":metadata.name"
}

get_all_pods() {
    local context=${1}
    local namespace=${2}
    kubectl --context="${context}" get pods --namespace "${namespace}" --no-headers -o custom-columns=":metadata.name"
}

is_mdb_resource_pod() {
    local context="${1}"
    local pod="${2}"
    local namespace="${3}"

    kubectl --context="${context}" exec "${pod}" -n "${namespace}" -- ls /var/log/mongodb-mms-automation/automation-agent-verbose.log &>/dev/null
}

# dump_pod_logs dumps agent and mongodb logs.
dump_pod_logs() {
    local context="${1}"
    local pod="${2}"
    local namespace="${3}"
    local prefix="${4}"

    if is_mdb_resource_pod "${context}" "${pod}" "${namespace}"; then
        # for MDB resource Pods, we dump the log files from the file system
        echo "Writing agent and mongodb logs for pod ${pod} to logs"
        kubectl --context="${context}" cp "${namespace}/${pod}:/var/log/mongodb-mms-automation/automation-agent-verbose.log" "logs/${prefix}${pod}-agent-verbose.log" &> /dev/null
        kubectl --context="${context}" cp "${namespace}/${pod}:/var/log/mongodb-mms-automation/automation-agent.log" "logs/${prefix}${pod}-agent.log" &> /dev/null
        kubectl --context="${context}" cp "${namespace}/${pod}:/var/log/mongodb-mms-automation/monitoring-agent-verbose.log" "logs/${prefix}${pod}-monitoring-agent-verbose.log" &> /dev/null
        kubectl --context="${context}" cp "${namespace}/${pod}:/var/log/mongodb-mms-automation/monitoring-agent.log" "logs/${prefix}${pod}-monitoring-agent.log" &> /dev/null
        kubectl --context="${context}" logs -n "${namespace}" "${pod}" -c "mongodb-agent-monitoring" > "logs/${prefix}${pod}-monitoring-agent-stdout.log" || true
        kubectl --context="${context}" logs -n "${namespace}" "${pod}" -c "mongod" > "logs/${prefix}${pod}-mongod-container.log" || true
        kubectl --context="${context}" logs -n "${namespace}" "${pod}" -c "mongodb-agent" > "logs/${prefix}${pod}-mongodb-agent-container.log" || true
        kubectl --context="${context}" cp "${namespace}/${pod}:/var/log/mongodb-mms-automation/mongodb.log" "logs/${prefix}${pod}-mongodb.log" &> /dev/null || true

        # note that this file may get empty if the logs have already grew too much - seems it's better to have it explicitly empty then just omit
        kubectl --context="${context}" logs -n "${namespace}" "${pod}" | jq -c -r 'select( .logType == "agent-launcher-script") | .contents' 2> /dev/null > "logs/${prefix}${pod}-launcher.log"
    else
        # for all other pods we want each log per container from kubectl
        for container in $(kubectl --context="${context}" get pods -n "${namespace}" "${pod}" -o jsonpath='{.spec.containers[*].name}'); do
            echo "Writing log file for pod ${pod} - container ${container} to logs/${prefix}${pod}-${container}.log"
            kubectl --context="${context}" logs -n "${namespace}" "${pod}" -c "${container}" > "logs/${prefix}${pod}-${container}.log"

            # Check if the container has restarted by examining its restart count
            restartCount=$(kubectl --context="${context}" get pod -n "${namespace}" "${pod}" -o jsonpath="{.status.containerStatuses[?(@.name=='${container}')].restartCount}")

            if [ "${restartCount}" -gt 0 ]; then
                echo "Writing log file for restarted ${pod} - container ${container} to logs/${prefix}${pod}-${container}-previous.log"
                kubectl --context="${context}" logs --previous -n "${namespace}" "${pod}" -c "${container}" > "logs/${prefix}${pod}-${container}-previous.log" || true
            fi

        done
    fi

    if kubectl --context="${context}" exec "${pod}" -n "${namespace}" -- ls /var/log/mongodb-mms-automation/automation-agent-stderr.log &>/dev/null; then
        kubectl --context="${context}" cp "${namespace}/${pod}:/var/log/mongodb-mms-automation/automation-agent-stderr.log" "logs/${prefix}${pod}-agent-stderr.log" &> /dev/null
    fi
}

# dump_pod_readiness_state dumps readiness and agent-health-status files.
dump_pod_readiness_state() {
    local context="${1}"
    local pod="${2}"
    local namespace="${3}"
    local prefix="${4}"

    # kubectl cp won't create any files if the file doesn't exist in the container
    agent_health_status="logs/${prefix}${pod}-agent-health-status.json"
    echo "Writing agent ${agent_health_status}"
    kubectl --context="${context}" cp -c "mongodb-agent" "${namespace}/${pod}:/var/log/mongodb-mms-automation/agent-health-status.json" "${agent_health_status}" &> /dev/null
    ([[ -f "${agent_health_status}" ]] && jq . < "${agent_health_status}" > tmpfile && mv tmpfile "${agent_health_status}")

    if [[ ! -f "${agent_health_status}" ]]; then
      echo "Agent health status not found; trying community health status: "
      kubectl --context="${context}" cp -c "mongodb-agent" "${namespace}/${pod}:/var/log/mongodb-mms-automation/healthstatus/agent-health-status.json" "${agent_health_status}" &> /dev/null
      ([[ -f "${agent_health_status}" ]] && jq . < "${agent_health_status}" > tmpfile && mv tmpfile "${agent_health_status}")
    fi

    kubectl --context="${context}" cp "${namespace}/${pod}:/var/log/mongodb-mms-automation/readiness.log" "logs/${prefix}${pod}-readiness.log" &> /dev/null
}

# dump_pod_config dumps mongod configuration and cluster-config.
dump_pod_config() {
    local context="${1}"
    local pod="${2}"
    local namespace="${3}"
    local prefix="${4}"

    # cluster-config.json is a mounted volume and the actual file is located in the "..data" directory
    pod_cluster_config="logs/${prefix}${pod}-cluster-config.json"
    kubectl --context="${context}" cp "${namespace}/${pod}:/var/lib/mongodb-automation/..data/cluster-config.json" "${pod_cluster_config}" &> /dev/null
    ([[ -f "${pod_cluster_config}" ]] && jq . < "${pod_cluster_config}" > tmpfile && mv tmpfile "${pod_cluster_config}")

    # Mongodb Configuration
    kubectl --context="${context}" cp "${namespace}/${pod}:data/automation-mongod.conf" "logs/${prefix}${pod}-automation-mongod.conf" &> /dev/null
}

dump_configmaps() {
    local context="${1}"
    local namespace="${2}"
    local prefix="${3}"
    kubectl --context="${context}" -n "${namespace}" get configmaps -o yaml > "logs/${prefix}z_configmaps.txt"
}

decode_secret() {
    local context=${1}
    local secret=${2}
    local namespace=${3}

    kubectl --context="${context}" get secret "${secret}" -o json -n "${namespace}" | jq -r '.data | with_entries(.value |= @base64d)' 2> /dev/null
}

dump_secrets() {
    local context="${1}"
    local namespace="${2}"
    local prefix="${3}"
    for secret in $(kubectl --context="${context}" get secrets -n "${namespace}" --no-headers | grep -v service-account-token | grep -v dockerconfigjson | awk '{ print $1 }'); do
        decode_secret "${context}" "${secret}" "${namespace}" > "logs/${prefix}z_secret_${secret}.txt"
    done
}

dump_services() {
    local context="${1}"
    local namespace="${2}"
    local prefix="${3}"
    kubectl --context="${context}" -n "${namespace}" get svc -o yaml > "logs/${prefix}z_services.txt"
}

dump_metrics() {
  local context="${1}"
  local namespace="${2}"
  local operator_pod="${3}"
  local prefix="${4}"
  kubectl --context="${context}" exec -it "${operator_pod}"  -n "${namespace}" -- curl localhost:8080/metrics > "logs/${prefix}metrics_${operator_pod}.txt"
}

# dump_pods writes logs for each relevant Pod in the namespace: agent, mongodb
# logs, etc.
dump_pods() {
    local context="${1}"
    local namespace="${2}"
    local prefix="${3}"

    pods=$(get_all_pods "${context}" "${namespace}")

    # we only have readiness and automationconfig in mdb pods
    for pod in ${pods}; do
        dump_pod_readiness_state "${context}" "${pod}" "${namespace}" "${prefix}"
        dump_pod_config "${context}" "${pod}" "${namespace}" "${prefix}"
    done

    # for all pods in the namespace we want to have logs and describe output
    echo "Iterating over pods to dump logs: ${pods}"
    for pod in ${pods}; do
        kubectl --context="${context}" describe "pod/${pod}" -n "${namespace}" > "logs/${prefix}${pod}-pod-describe.txt"
        dump_pod_logs "${context}" "${pod}" "${namespace}" "${prefix}"
    done

    if  (kubectl --context="${context}" get pod -n "${namespace}" -l app.kubernetes.io/name=controller ) &> /dev/null ; then
        operator_pod=$(kubectl --context="${context}" get pod -n "${namespace}" -l app.kubernetes.io/component=controller --no-headers -o custom-columns=":metadata.name")
        if [ -n "${operator_pod}" ]; then
          kubectl --context="${context}" describe "pod/${operator_pod}" -n "${namespace}" > "logs/${prefix}z_${operator_pod}-pod-describe.txt"
          dump_metrics "${context}" "${namespace}" "${operator_pod}" "${prefix}"
        fi

    fi
}

# dump_diagnostics writes only the *most important information* for debugging
# tests, no more. Ideally the diagnostics file is as small as possible. Avoid
# high density of information; the main objective of this file is to direct you
# to a place where to find your problem, not to tell you what the problem is.
dump_diagnostics() {
    local context="${1}"
    local namespace="${2}"

    header "All namespace resources"
    kubectl --context="${context}" get all -n "${namespace}"

    dump_objects "${context}" mongodb "MongoDB Resources" "${namespace}" "get -o yaml"
    dump_objects "${context}" mongodbcommunity "MongoDBCommunity Resources" "${namespace}" "get -o yaml"
    dump_objects "${context}" mongodbusers "MongoDBUser Resources" "${namespace}" "get -o yaml"
    dump_objects "${context}" opsmanagers "MongoDBOpsManager Resources" "${namespace}" "get -o yaml"
    dump_objects "${context}" mongodbmulticluster "MongoDB Multi Resources" "${namespace}" "get -o yaml"
    dump_objects "${context}" mongodbsearch "MongoDB Search Resources" "${namespace}" "get -o yaml"
}

download_test_results() {
    local context="${1}"
    local namespace="${2}"
    local test_pod_name="${3:-e2e-test}"

    echo "Downloading test results from ${test_pod_name} pod"

    # Try to copy from shared volume using the keepalive container
    if kubectl --context="${context}" cp "${namespace}/${test_pod_name}:/tmp/results/result.suite" "logs/result.suite" -c keepalive 2>/dev/null; then
        echo "Successfully downloaded result.suite from test pod"
    else
        echo "Could not find result.suite via direct copy"
        # Get logs from the test container
        kubectl --context="${context}" logs -n "${namespace}" "${test_pod_name}" -c e2e-test > "logs/result.suite" 2>/dev/null
    fi
}

# dump_events gets all events from a namespace and saves them to a file
dump_events() {
    local context="${1}"
    local namespace="${2}"
    local prefix="${3}"

    echo "Collecting events for namespace ${namespace}"
    # Sort by lastTimestamp to have the most recent events at the top
    kubectl --context="${context}" get events --sort-by='.lastTimestamp' -n "${namespace}" > "logs/${prefix}events.txt"

    # Also get events in yaml format for more details
    kubectl --context="${context}" get events -n "${namespace}" -o yaml > "logs/${prefix}events_detailed.yaml"
}

# dump_namespace dumps a namespace, diagnostics, logs and generic Kubernetes
# resources.
dump_namespace() {
    local context=${1}
    local namespace=${2}
    local prefix="${3}_${namespace}_"

    # do not fail for any reason
    set +e

    # 1. Dump diagnostic information
    # gathers the information about K8s objects and writes it to the file which will be attached to Evergreen job
    mkdir -p logs

    # 2. Write diagnostics file
    dump_diagnostics "${context}" "${namespace}" > "logs/${prefix}0_diagnostics.txt"

    # 3. Print Pod logs
    dump_pods "${context}" "${namespace}" "${prefix}"

    # 4. Print other Kubernetes resources
    dump_configmaps "${context}" "${namespace}" "${prefix}"
    dump_secrets "${context}" "${namespace}" "${prefix}"
    dump_services "${context}" "${namespace}" "${prefix}"
    dump_events "${context}" "${namespace}" "${prefix}"

    # Download test results from the test pod in community
    download_test_results "${context}" "${namespace}" "e2e-test"

    dump_objects "${context}" pvc "Persistent Volume Claims" "${namespace}" "get -o yaml" "logs/${prefix}z_persistent_volume_claims.txt"
    dump_objects "${context}" deploy "Deployments" "${namespace}" "get -o yaml" "logs/${prefix}z_deployments.txt"
    dump_objects "${context}" deploy "Deployments" "${namespace}" "describe" "logs/${prefix}z_deployments_describe.txt"
    dump_objects "${context}" sts "StatefulSets" "${namespace}" "describe" "logs/${prefix}z_statefulsets.txt"
    dump_objects "${context}" sts "StatefulSets Yaml" "${namespace}" "get -o yaml" "logs/${prefix}z_statefulsets.txt"
    dump_objects "${context}" serviceaccounts "ServiceAccounts" "${namespace}" "get -o yaml" "logs/${prefix}z_service_accounts.txt"
    dump_objects "${context}" clusterrolebindings "ClusterRoleBindings" "${namespace}" "get -o yaml" "logs/${prefix}z_clusterrolebindings.txt"
    dump_objects "${context}" clusterroles "ClusterRoles" "${namespace}" "get -o yaml" "logs/${prefix}z_clusterroles.txt"
    dump_objects "${context}" rolebindings "RoleBindings" "${namespace}" "get -o yaml" "logs/${prefix}z_rolebindings.txt"
    dump_objects "${context}" roles "Roles" "${namespace}" "get -o yaml" "logs/${prefix}z_roles.txt"
    dump_objects "${context}" validatingwebhookconfigurations "Validating Webhook Configurations" "${namespace}" "get -o yaml" "logs/${prefix}z_validatingwebhookconfigurations.txt"
    dump_objects "${context}" certificates.cert-manager.io "Cert-manager certificates" "${namespace}" "get -o yaml" "logs/${prefix}z_certificates_certmanager.txt" 2> /dev/null
    dump_objects "${context}" catalogsources "OLM CatalogSources" "${namespace}" "get -o yaml" "logs/${prefix}z_olm_catalogsources.txt" 2> /dev/null
    dump_objects "${context}" operatorgroups "OLM OperatorGroups" "${namespace}" "get -o yaml" "logs/${prefix}z_olm_operatorgroups.txt" 2> /dev/null
    dump_objects "${context}" subscriptions "OLM Subscriptions" "${namespace}" "get -o yaml" "logs/${prefix}z_olm_subscriptions.txt" 2> /dev/null
    dump_objects "${context}" installplans "OLM InstallPlans" "${namespace}" "get -o yaml" "logs/${prefix}z_olm_installplans.txt" 2> /dev/null
    dump_objects "${context}" clusterserviceversions "OLM ClusterServiceVersions" "${namespace}" "get -o yaml" "logs/${prefix}z_olm_clusterserviceversions.txt" 2> /dev/null
    dump_objects "${context}" pods "Pods" "${namespace}" "get -o yaml" "logs/${prefix}z_pods.txt" 2> /dev/null

    kubectl --context="${context}" get crd -o name
    # shellcheck disable=SC2046
    kubectl --context="${context}" describe $(kubectl --context="${context}" get crd -o name | grep mongodb) > "logs/${prefix}z_mongodb_crds.log"

    kubectl --context="${context}" describe nodes > "logs/${prefix}z_nodes_detailed.log" || true
}
