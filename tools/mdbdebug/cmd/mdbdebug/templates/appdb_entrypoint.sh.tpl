#!/usr/bin/env bash

set -Eeou pipefail

{{ template "common.sh.tpl" }}

set -x

tls_enabled="{{.TLSEnabled}}"
static_arch="{{.StaticArch}}"
container_name="{{.ContainerName}}"
mongosh_container_name="mongodb-agent"

if [[ "${static_arch}" == "true" ]]; then
  mongosh_path='/bin/mongosh'
else
  mongosh_path='/var/lib/mongodb-mms-automation/mongosh-linux-x86_64-2.2.4/bin/mongosh'
fi

sigterm() {
  exit 0
}
trap sigterm SIGTERM

export LANG=en_GB.UTF-8
tmuxp load -d -y "/scripts/session.yaml"

base_log_dir="{{.BaseLogDir}}"
base_dir="${base_log_dir}/mdb-debug"
mongod_container_log_file="${base_log_dir}/mongod_container.log"
agent_container_log_file="${base_log_dir}/agent_container.log"

cluster_config_file="${base_dir}/ac/cluster-config.json"
health_file="${base_dir}/health/health.json"
pod_file="${base_dir}/pod/pod.json"
sts_file="${base_dir}/sts/sts.json"
readiness_file="${base_dir}/readiness/readiness.log.json"
state_file="${base_dir}/state/state.json"
mongod_config_file="${base_dir}/mongod_config/config.json"
cr_file="${base_dir}/cr/cr.json"

# we have to wait for the db pod to create /data/logs otherwise we create it with incorrect permissions
while [ ! -d "${base_log_dir}" ]; do
  echo "Waiting for ${base_log_dir} to be initialized by the db pod"
  sleep 1
done

mkdir -p "$(dirname "${cluster_config_file}")"
mkdir -p "$(dirname "${health_file}")"
mkdir -p "$(dirname "${pod_file}")"
mkdir -p "$(dirname "${sts_file}")"
mkdir -p "$(dirname "${readiness_file}")"
mkdir -p "$(dirname "${mongod_config_file}")"
mkdir -p "$(dirname "${cr_file}")"

tail_pod_log "{{.Namespace}}" "{{.PodName}}" "mongod" "${mongod_container_log_file}" &
tail_pod_log "{{.Namespace}}" "{{.PodName}}" "mongodb-agent" "${agent_container_log_file}" &

set +e
while true; do
  kubectl_get_json "{{.Namespace}}" "{{.ResourceType}}" "{{.ResourceName}}" "${cr_file}" 2>&1 | prepend "get_{{.ResourceType}}_cr"

  kubectl exec -n "{{.Namespace}}" -c "mongodb-agent" "{{.PodName}}" -- cat /var/log/mongodb-mms-automation/healthstatus/agent-health-status.json | jq . >${health_file}.tmp
  mv ${health_file}.tmp ${health_file}

  kubectl get pod -n "{{.Namespace}}" "{{.PodName}}" -o json >${pod_file}.tmp
  mv ${pod_file}.tmp ${pod_file}

  kubectl get sts -n "{{.Namespace}}" "{{.StsName}}" -o json >${sts_file}.tmp
  mv ${sts_file}.tmp ${sts_file}

  tail -n 100 "${base_log_dir}/readiness.log" | jq --color-output -c '.' >"${readiness_file}"

  cp "/data/ac/cluster-config.json" "${cluster_config_file}"

  kubectl_get_state_json "{{.Namespace}}" "{{.ResourceName}}" "${state_file}"

  get_file "{{.Namespace}}" "{{.PodName}}" "mongod" "/data/automation-mongod.conf" "${mongod_config_file}.yaml.tmp"
  yq . "${mongod_config_file}.yaml.tmp" -o json >"${mongod_config_file}.tmp"
  mv "${mongod_config_file}.tmp" "${mongod_config_file}"

  sleep 1
done
