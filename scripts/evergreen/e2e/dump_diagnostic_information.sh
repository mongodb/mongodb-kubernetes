#!/usr/bin/env bash

set -Eeou pipefail

## We need to make sure this script does not fail if one of
## the kubectl commands fails.
set +e

source scripts/funcs/printing

dump_all() {
    [[ "${MODE-}" = "dev" ]] && return

    # TODO: provide a cleaner way of handling this. For now we run the same command with kubectl configured
    # with a different context.
    local original_context
    original_context="$(kubectl config current-context)"
    kubectl config use-context "${1:-${original_context}}" &> /dev/null
    prefix="${1:-${original_context}}_"
    # shellcheck disable=SC2154
    if [[ "${KUBE_ENVIRONMENT_NAME:-}" != "multi" ]]; then
        prefix=""
    fi

    # The dump process usually happens for a single namespace (the one the test and the operator are installed to)
    # but in some exceptional cases (e.g. clusterwide operator) there can be more than 1 namespace to print diagnostics
    # In this case the python test app may create the test namespace and add necessary labels and annotations so they
    # would be dumped for diagnostics as well
    # TODO: MCK mco not all
    for ns in $(kubectl get namespace --output=jsonpath={.items..metadata.name}); do
        if kubectl get namespace "${ns}" -o jsonpath='{.metadata.annotations}' | grep -q "${task_id:?}"; then
            echo "Dumping all diagnostic information for namespace ${ns}"
            dump_namespace "${ns}" "${prefix}"
        fi
    done

    if kubectl get namespace "olm" &>/dev/null; then
      echo "Dumping olm namespace"
      dump_namespace "olm" "olm"
    fi

    kubectl config use-context "${original_context}" &> /dev/null

    kubectl -n "kube-system" get configmap coredns -o yaml > "logs/${prefix}coredns.yaml"
}

dump_objects() {
    local object=$1
    local msg=$2
    local namespace=${3}
    local action=${4:-get -o yaml}

    if [ "$(kubectl get "${object}" --no-headers -o name -n "${namespace}" | wc -l)" = "0" ]; then
        # if no objects of this type, return
        return
    fi

    header "${msg}"
    # shellcheck disable=SC2086
    kubectl -n "${namespace}" ${action} "${object}" 2>&1
}

# get_operator_managed_pods returns a list of names of the Pods that are managed
# by the Operator.
get_operator_managed_pods() {
    local namespace=${1}
    kubectl get pods --namespace "${namespace}" --selector=controller=mongodb-enterprise-operator --no-headers -o custom-columns=":metadata.name"
}

get_all_pods() {
    local namespace=${1}
    kubectl get pods --namespace "${namespace}" --no-headers -o custom-columns=":metadata.name"
}

is_mdb_resource_pod() {
    local pod="${1}"
    local namespace="${2}"

    kubectl exec "${pod}" -n "${namespace}" -- ls /var/log/mongodb-mms-automation/automation-agent-verbose.log &>/dev/null
}

# dump_pod_logs dumps agent and mongodb logs.
dump_pod_logs() {
    local pod="${1}"
    local namespace="${2}"
    local prefix="${3}"

    if is_mdb_resource_pod "${pod}" "${namespace}"; then
        # for MDB resource Pods, we dump the log files from the file system
        echo "Writing agent and mongodb logs for pod ${pod} to logs"
        kubectl cp "${namespace}/${pod}:var/log/mongodb-mms-automation/automation-agent-verbose.log" "logs/${prefix}${pod}-agent-verbose.log" &> /dev/null
        tail -n 500 "logs/${pod}-agent-verbose.log" > "logs/${prefix}${pod}-agent.log" || true
        kubectl cp "${namespace}/${pod}:var/log/mongodb-mms-automation/monitoring-agent-verbose.log" "logs/${prefix}${pod}-monitoring-agent-verbose.log" &> /dev/null
        kubectl cp "${namespace}/${pod}:var/log/mongodb-mms-automation/monitoring-agent.log" "logs/${prefix}${pod}-monitoring-agent.log" &> /dev/null
        kubectl logs -n "${namespace}" "${pod}" -c "mongodb-agent-monitoring" > "logs/${prefix}${pod}-monitoring-agent-stdout.log" || true
        kubectl cp "${namespace}/${pod}:var/log/mongodb-mms-automation/mongodb.log" "logs/${prefix}${pod}-mongodb.log" &> /dev/null || true
        # note that this file may get empty if the logs have already grew too much - seems it's better to have it explicitly empty then just omit
        kubectl logs -n "${namespace}" "${pod}" | jq -c -r 'select( .logType == "agent-launcher-script") | .contents' 2> /dev/null > "logs/${prefix}${pod}-launcher.log"
    else
        # for all other pods we want each log per container from kubectl
        for container in $(kubectl get pods -n "${namespace}" "${pod}" -o jsonpath='{.spec.containers[*].name}'); do
            echo "Writing log file for pod ${pod} - container ${container} to logs/${pod}-${container}.log"
            kubectl logs -n "${namespace}" "${pod}" -c "${container}" > "logs/${pod}-${container}.log"

            # Check if the container has restarted by examining its restart count
            restartCount=$(kubectl get pod -n "${namespace}" "${pod}" -o jsonpath="{.status.containerStatuses[?(@.name=='${container}')].restartCount}")

            if [ "${restartCount}" -gt 0 ]; then
                echo "Writing log file for restarted ${pod} - container ${container} to logs/${pod}-${container}-previous.log"
                kubectl logs --previous -n "${namespace}" "${pod}" -c "${container}" > "logs/${pod}-${container}-previous.log" || true
            fi

        done
    fi

    if kubectl exec "${pod}" -n "${namespace}" -- ls /var/log/mongodb-mms-automation/automation-agent-stderr.log &>/dev/null; then
        kubectl cp "${namespace}/${pod}:var/log/mongodb-mms-automation/automation-agent-stderr.log" "logs/${prefix}${pod}-agent-stderr.log" &> /dev/null
    fi
}

# dump_pod_readiness_state dumps readiness and agent-health-status files.
dump_pod_readiness_state() {
    local pod="${1}"
    local namespace="${2}"
    local prefix="${3}"

    # kubectl cp won't create any files if the file doesn't exist in the container
    agent_health_status="logs/${prefix}${pod}-agent-health-status.json"
    kubectl cp "${namespace}/${pod}:var/log/mongodb-mms-automation/agent-health-status.json" "${agent_health_status}" &> /dev/null
    ([[ -f "${agent_health_status}" ]] && jq . < "${agent_health_status}" > tmpfile && mv tmpfile "${agent_health_status}")

    kubectl cp "${namespace}/${pod}:var/log/mongodb-mms-automation/readiness.log" "logs/${prefix}${pod}-readiness.log" &> /dev/null
}

# dump_pod_config dumps mongod configuration and cluster-config.
dump_pod_config() {
    local pod="${1}"
    local namespace="${2}"
    local prefix="${3}"

    # cluster-config.json is a mounted volume and the actual file is located in the "..data" directory
    pod_cluster_config="logs/${prefix}${pod}-cluster-config.json"
    kubectl cp "${namespace}/${pod}:var/lib/mongodb-automation/..data/cluster-config.json" "${pod_cluster_config}" &> /dev/null
    ([[ -f "${pod_cluster_config}" ]] && jq . < "${pod_cluster_config}" > tmpfile && mv tmpfile "${pod_cluster_config}")

    # Mongodb Configuration
    kubectl cp "${namespace}/${pod}:data/automation-mongod.conf" "logs/${prefix}${pod}-automation-mongod.conf" &> /dev/null
}

dump_configmaps() {
    local namespace="${1}"
    local prefix="${2}"
    kubectl -n "${namespace}" get configmaps -o yaml > "logs/${prefix}z_configmaps.txt"
}

decode_secret() {
    local secret=${1}
    local namespace=${2}

    kubectl get secret "${secret}" -o json -n "${namespace}" | jq -r '.data | with_entries(.value |= @base64d)' 2> /dev/null
}

dump_secrets() {
    local namespace="${1}"
    local prefix="${2}"
    for secret in $(kubectl get secrets -n "${namespace}" --no-headers | grep -v service-account-token | grep -v dockerconfigjson | awk '{ print $1 }'); do
        decode_secret "${secret}" "${namespace}" > "logs/${prefix}z_secret_${secret}.txt"
    done
}

dump_services() {
    local namespace="${1}"
    local prefix="${2}"
    kubectl -n "${namespace}" get svc -o yaml > "logs/${prefix}z_services.txt"
}

dump_metrics() {
  local namespace="${1}"
  local operator_pod="${2}"
  kubectl exec -it "${operator_pod}"  -n "${namespace}" -- curl localhost:8080/metrics > "logs/metrics_${operator_pod}.txt"
}

# dump_pods writes logs for each relevant Pod in the namespace: agent, mongodb
# logs, etc.
dump_pods() {
    local namespace="${1}"
    local prefix="${2}"

    pods=$(get_all_pods "${namespace}")
    operator_managed_pods=$(get_operator_managed_pods "${namespace}")

    # we only have readiness and automationconfig in mdb pods
    for pod in ${operator_managed_pods}; do
        dump_pod_readiness_state "${pod}" "${namespace}" "${prefix}"
        dump_pod_config "${pod}" "${namespace}" "${prefix}"
    done

    # for all pods in the namespace we want to have logs and describe output
    for pod in ${pods}; do
        kubectl describe "pod/${pod}" -n "${namespace}" > "logs/${prefix}${pod}-pod-describe.txt"
        dump_pod_logs "${pod}" "${namespace}" "${prefix}"
    done

    if  (kubectl get pod -n "${namespace}" -l app.kubernetes.io/name=controller ) &> /dev/null ; then
        operator_pod=$(kubectl get pod -n "${namespace}" -l app.kubernetes.io/component=controller --no-headers -o custom-columns=":metadata.name")
        if [ -n "${operator_pod}" ]; then
          kubectl describe "pod/${operator_pod}" -n "${namespace}" > "logs/z_${operator_pod}-pod-describe.txt"
          dump_metrics "${namespace}" "${operator_pod}"
        fi

    fi
}

# dump_diagnostics writes only the *most important information* for debugging
# tests, no more. Ideally the diagnostics file is as small as possible. Avoid
# high density of information; the main objective of this file is to direct you
# to a place where to find your problem, not to tell you what the problem is.
dump_diagnostics() {
    local namespace="${1}"

    dump_objects mongodb "MongoDB Resources" "${namespace}"
    dump_objects mongodbusers "MongoDBUser Resources" "${namespace}"
    dump_objects opsmanagers "MongoDBOpsManager Resources" "${namespace}"
    dump_objects mongodbmulticluster "MongoDB Multi Resources" "${namespace}"
    dump_objects mongodbcommunity "MongoDB Community Resources" "${namespace}"

    header "All namespace resources"
    kubectl get all -n "${namespace}"
}

# dump_namespace dumps a namespace, diagnostics, logs and generic Kubernetes
# resources.
dump_namespace() {
    local namespace=${1}
    local prefix="${2}"

    # do not fail for any reason
    set +e

    # 1. Dump diagnostic information
    # gathers the information about K8s objects and writes it to the file which will be attached to Evergreen job
    mkdir -p logs

    # 2. Write diagnostics file
    dump_diagnostics "${namespace}"  > "logs/${prefix}0_diagnostics.txt"

    # 3. Print Pod logs
    dump_pods "${namespace}" "${prefix}"

    # 4. Print other Kubernetes resources
    dump_configmaps "${namespace}" "${prefix}"
    dump_secrets "${namespace}" "${prefix}"
    dump_services "${namespace}" "${prefix}"

    dump_objects pvc "Persistent Volume Claims" "${namespace}"  > "logs/${prefix}z_persistent_volume_claims.txt"
    dump_objects deploy "Deployments" "${namespace}" > "logs/${prefix}z_deployments.txt"
    dump_objects deploy "Deployments" "${namespace}" "describe" > "logs/${prefix}z_deployments_describe.txt"
    dump_objects sts "StatefulSets" "${namespace}" describe > "logs/${prefix}z_statefulsets.txt"
    dump_objects sts "StatefulSets Yaml" "${namespace}" >> "logs/${prefix}z_statefulsets.txt"
    dump_objects serviceaccounts "ServiceAccounts" "${namespace}" > "logs/${prefix}z_service_accounts.txt"
    dump_objects validatingwebhookconfigurations "Validating Webhook Configurations" "${namespace}" > "logs/${prefix}z_validatingwebhookconfigurations.txt"
    dump_objects certificates.cert-manager.io "Cert-manager certificates" "${namespace}"  2> /dev/null > "logs/${prefix}z_certificates_certmanager.txt"
    dump_objects catalogsources "OLM CatalogSources" "${namespace}"  2> /dev/null > "logs/${prefix}z_olm_catalogsources.txt"
    dump_objects operatorgroups "OLM OperatorGroups" "${namespace}"  2> /dev/null > "logs/${prefix}z_olm_operatorgroups.txt"
    dump_objects subscriptions "OLM Subscriptions" "${namespace}"  2> /dev/null > "logs/${prefix}z_olm_subscriptions.txt"
    dump_objects installplans "OLM InstallPlans" "${namespace}"  2> /dev/null > "logs/${prefix}z_olm_installplans.txt"
    dump_objects clusterserviceversions "OLM ClusterServiceVersions" "${namespace}"  2> /dev/null > "logs/${prefix}z_olm_clusterserviceversions.txt"
    dump_objects pods "Pods" "${namespace}"  2> /dev/null > "logs/${prefix}z_pods.txt"

    kubectl get crd -o name
    # shellcheck disable=SC2046
    kubectl describe $(kubectl get crd -o name | grep mongodb) > "logs/${prefix}z_mongodb_crds.log"
}
