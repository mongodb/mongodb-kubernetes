#!/usr/bin/env bash

set -Eeou pipefail

set -x

{{ template "common.sh.tpl" }}

tls_enabled="{{.TLSEnabled}}"
static_arch="{{.StaticArch}}"
container_name="{{.ContainerName}}"
pod_fqdn="{{.PodFQDN}}"
mongosh_container_name="mongodb-enterprise-database"

if [[ "${static_arch}" == "true" ]]; then
  mongosh_path='/bin/mongosh'
else
  mongosh_path='/var/lib/mongodb-mms-automation/mongosh-linux-x86_64-2.2.4/bin/mongosh'
fi


tmuxp load -d -y "/scripts/session.yaml"
base_log_dir="/data/logs"
base_dir="/data/logs/mdb-debug"

logs_dir="${base_dir}/logs"
cr_file="${base_dir}/mdb/mdb.json"
pod_file="${base_dir}/pod/pod.json"
sts_file="${base_dir}/sts/sts.json"
health_file="${base_dir}/health/health.json"
readiness_file="${base_dir}/readiness/readiness.log.json"
cluster_config_file="${base_dir}/ac/cluster-config.json"
cluster_config_tmp_file="${base_dir}/ac_tmp/cluster-config.json"
rs_config_file="${base_dir}/rs/config.json"
rs_hello_file="${base_dir}/rs_hello/hello.json"
mongod_config_file="${base_dir}/mongod_config/config.json"
state_file="${base_dir}/state/state.json"

# we have to wait for the db pod to create /data/logs otherwise we create it with incorrect permissions
while [ ! -d "${base_log_dir}" ]; do
  echo "Waiting for ${base_log_dir} to be initialized by the db pod"
  sleep 1
done

mkdir -p "${logs_dir}"
mkdir -p "$(dirname "${cr_file}")"
mkdir -p "$(dirname "${pod_file}")"
mkdir -p "$(dirname "${sts_file}")"
mkdir -p "$(dirname "${cluster_config_file}")"
mkdir -p "$(dirname "${cluster_config_tmp_file}")"
mkdir -p "$(dirname "${rs_config_file}")"
mkdir -p "$(dirname "${rs_hello_file}")"
mkdir -p "$(dirname "${health_file}")"
mkdir -p "$(dirname "${mongod_config_file}")"
mkdir -p "$(dirname "${state_file}")"

# TODO read env vars with log file names
pod_log_file="pod.log"

tail_pod_log "{{.Namespace}}" "{{.PodName}}" "${container_name}" "${logs_dir}/${pod_log_file}" &

org_id=""
project_name=""
base_url=""
project_id=""
agent_api_key=""

set +e
while true; do
  kubectl_get_json "{{.Namespace}}" "mdb" "{{.ResourceName}}" "${cr_file}"
  kubectl_get_json "{{.Namespace}}" "pod" "{{.PodName}}" "${pod_file}"
  kubectl_get_json "{{.Namespace}}" "sts" "{{.StsName}}" "${sts_file}"

  get_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "/data/automation-mongod.conf" "${mongod_config_file}.yaml.tmp"
  yq . "${mongod_config_file}.yaml.tmp" -o json >"${mongod_config_file}.tmp"
  mv "${mongod_config_file}.tmp" "${mongod_config_file}"

  mongod_port=27017
  if [[ -f "${mongod_config_file}" ]]; then
    mongod_port=$(jq -r '.net.port' "${mongod_config_file}")
  fi

  get_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "/var/log/mongodb-mms-automation/readiness.log" "${readiness_file}.tmp"
  mv "${readiness_file}.tmp" "${readiness_file}"

  if [[ -z ${agent_api_key} ]]; then
    IFS='|' read -r org_id base_url project_id agent_api_key <<<"$(get_project_data2 "${namespace}" "{{.ResourceName}}" "{{.PodName}}")"
    echo "org_id: ${org_id}"
    echo "base_url: ${base_url}"
    echo "project_id: ${project_id}"
    echo "agent_api_key: ${agent_api_key}"
  fi

  if [[ -n ${agent_api_key} ]]; then
    get_ac "${namespace}" "${base_url}" "${project_id}" "${agent_api_key}" >"${cluster_config_file}.tmp"
    mv "${cluster_config_file}.tmp" "${cluster_config_file}"
  fi
  get_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "/tmp/mongodb-mms-automation-cluster-backup.json" "${cluster_config_tmp_file}"

  tls_args=""
  if [[ "${tls_enabled}" == "true" ]]; then
    tls_args="--tls --tlsCAFile /mongodb-automation/tls/ca/ca-pem"
  fi
  cmd="${mongosh_path} --host {{.PodFQDN}} --port ${mongod_port} --eval 'JSON.stringify(rs.config())' --quiet"
  exec_cmd "{{.Namespace}}" "{{.PodName}}" "${mongosh_container_name}" "${cmd}" | grep -v "Warning: Could not access" >"${rs_config_file}.tmp"
  mv "${rs_config_file}.tmp" "${rs_config_file}"

  cmd="${mongosh_path} --host {{.PodFQDN}} --port ${mongod_port} --eval 'JSON.stringify(db.hello())' --quiet"
  exec_cmd "{{.Namespace}}" "{{.PodName}}" "${mongosh_container_name}" "${cmd}" | grep -v "Warning: Could not access" >"${rs_hello_file}.tmp"
  mv "${rs_hello_file}.tmp" "${rs_hello_file}"

  cp /data/logs/agent-health-status.json /data/logs/mdb-debug/health/health.json.tmp
  mv /data/logs/mdb-debug/health/health.json.tmp /data/logs/mdb-debug/health/health.json

  kubectl_get_state_json "{{.Namespace}}" "{{.ResourceName}}" "${state_file}"

  sleep 1
done
