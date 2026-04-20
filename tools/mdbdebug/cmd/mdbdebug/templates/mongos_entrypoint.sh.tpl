#!/usr/bin/env bash

set -Eeou pipefail

set -x

{{ template "common.sh.tpl" }}

tls_enabled="{{.TLSEnabled}}"
static_arch="{{.StaticArch}}"
container_name="{{.ContainerName}}"
mongosh_container_name="mongodb-enterprise-database"

if [[ "${static_arch}" == "true" ]]; then
  mongosh_path='/bin/mongosh'
else
  mongosh_path='/var/lib/mongodb-mms-automation/mongosh-linux-x86_64-2.2.4/bin/mongosh'
fi


tmuxp load -d -y "/scripts/session.yaml"
base_dir="/data/logs/mdb-debug"

logs_dir="${base_dir}/logs"
cr_file="${base_dir}/mdb/mdb.json"
pod_file="${base_dir}/pod/pod.json"
sts_file="${base_dir}/sts/sts.json"
health_file="${base_dir}/health/health.json"
readiness_file="${base_dir}/readiness/readiness.log.json"
cluster_config_file="${base_dir}/ac/cluster-config.json"
cluster_config_tmp_file="${base_dir}/ac_tmp/cluster-config.json"
sh_status_file="${base_dir}/sh/status.json"
mongod_config_file="${base_dir}/mongod_config/config.json"

mkdir -p "${logs_dir}"
mkdir -p "$(dirname "${cr_file}")"
mkdir -p "$(dirname "${pod_file}")"
mkdir -p "$(dirname "${sts_file}")"
mkdir -p "$(dirname "${cluster_config_file}")"
mkdir -p "$(dirname "${cluster_config_tmp_file}")"
mkdir -p "$(dirname "${sh_status_file}")"
mkdir -p "$(dirname "${mongod_config_file}")"

# TODO read env vars with log file names
pod_log_file="pod.log"
mongo_log_file="mongodb.log"
automation_stderr_log_file="automation-agent-stderr.log"
automation_verbose_log_file="automation-agent-verbose.log"
automation_log_file="automation-agent.log"
backup_agent="backup-agent.log"
monitoring_log_file="monitoring-agent.log"

tail_pod_log "{{.Namespace}}" "{{.PodName}}" "${container_name}" "${logs_dir}/${pod_log_file}" &
tail_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "/var/log/mongodb-mms-automation/${mongo_log_file}" "${logs_dir}/${mongo_log_file}" &
tail_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "/var/log/mongodb-mms-automation/${automation_stderr_log_file}" "${logs_dir}/${automation_stderr_log_file}" &
tail_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "/var/log/mongodb-mms-automation/${automation_verbose_log_file}" "${logs_dir}/${automation_verbose_log_file}" &
tail_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "/var/log/mongodb-mms-automation/${automation_log_file}" "${logs_dir}/${automation_log_file}" &
tail_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "/var/log/mongodb-mms-automation/${backup_agent}" "${logs_dir}/${backup_agent}" &
tail_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "/var/log/mongodb-mms-automation/${monitoring_log_file}" "${logs_dir}/${monitoring_log_file}" &

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

  mongos_conf_path=$(exec_cmd "{{.Namespace}}" "{{.PodName}}" "${container_name}" 'ls /var/lib/mongodb-mms-automation/workspace/*.conf | head -n 1')
  get_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "${mongos_conf_path}" "${mongod_config_file}.yaml.tmp"
  yq . "${mongod_config_file}.yaml.tmp" -o json >"${mongod_config_file}.tmp"
  mv "${mongod_config_file}.tmp" "${mongod_config_file}"

  mongod_port=27017
  if [[ -f "${mongod_config_file}" ]]; then
    mongod_port=$(jq -r '.net.port' "${mongod_config_file}")
  fi

  get_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "/var/log/mongodb-mms-automation/agent-health-status.json" "${health_file}.tmp"
  mv "${health_file}.tmp" "${health_file}"

  get_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "/var/log/mongodb-mms-automation/readiness.log" "${readiness_file}.tmp"
  mv "${readiness_file}.tmp" "${readiness_file}"

  if [[ -z ${agent_api_key} ]]; then
    IFS='|' read -r org_id project_name base_url project_id agent_api_key <<<"$(get_project_data "${namespace}" "{{.ResourceName}}")"
    echo "org_id: ${org_id}"
    echo "project_name: ${project_name}"
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
  cmd="${mongosh_path} --host {{.PodName}}.{{.ResourceName}}-svc.{{.Namespace}}.svc.cluster.local --port ${mongod_port} ${tls_args} --eval 'JSON.stringify(sh.status())' --quiet"
  exec_cmd "{{.Namespace}}" "{{.PodName}}" "${mongosh_container_name}" "${cmd}" | grep -v "Warning: Could not access" >"${sh_status_file}.tmp"
  mv "${sh_status_file}.tmp" "${sh_status_file}"

  sleep 1
done
