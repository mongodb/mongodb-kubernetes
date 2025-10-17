#!/usr/bin/env bash

set -Eeou pipefail

set -x

{{ template "common.sh.tpl" }}

tls_enabled="{{.TLSEnabled}}"
static_arch="{{.StaticArch}}"
container_name="{{.ContainerName}}"

tmuxp load -d -y "/scripts/session.yaml"
base_dir="/data/logs/mdb-debug"

logs_dir="${base_dir}/logs"
cr_file="${base_dir}/om/om.json"
pod_file="${base_dir}/pod/pod.json"
sts_file="${base_dir}/sts/sts.json"
state_file="${base_dir}/state/state.json"

mms_migration_log_file="mms-migration.log"
mms_access_log_file="mms0-access.log"
mms_startup_log_file="mms0-startup.log"
mms_log_file="mms0.log"
pod_log="pod.log"

set +e

mkdir -p "${logs_dir}"
mkdir -p "$(dirname "${cr_file}")"
mkdir -p "$(dirname "${pod_file}")"
mkdir -p "$(dirname "${sts_file}")"
mkdir -p "$(dirname "${state_file}")"

echo "Starting tailing"
(tail_pod_log "{{.Namespace}}" "{{.PodName}}" "${container_name}" "${logs_dir}/${pod_log}" 2>&1 | prepend "tail_pod_log") &
(tail_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "/mongodb-ops-manager/logs/${mms_migration_log_file}" "${logs_dir}/${mms_migration_log_file}" 2>&1 | prepend "tail_file_mms_migration_log_file") &
(tail_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "/mongodb-ops-manager/logs/${mms_access_log_file}" "${logs_dir}/${mms_access_log_file}" 2>&1 | prepend "tail_file_mms_access_log_file") &
(tail_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "/mongodb-ops-manager/logs/${mms_startup_log_file}" "${logs_dir}/${mms_startup_log_file}" 2>&1 | prepend "tail_file_mms_startup_log_file") &
(tail_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "/mongodb-ops-manager/logs/${mms_log_file}" "${logs_dir}/${mms_log_file}" 2>&1 | prepend "tail_file_mms_log_file") &


while true; do
  echo "loop iteration"
  kubectl_get_json "{{.Namespace}}" "om" "{{.ResourceName}}" "${cr_file}" 2>&1 | prepend "get_json_om"
  kubectl_get_json "{{.Namespace}}" "pod" "{{.PodName}}" "${pod_file}" 2>&1 | prepend "get_json_pod"
  kubectl_get_json "{{.Namespace}}" "sts" "{{.StsName}}" "${sts_file}" 2>&1 | prepend "get_json_sts"
  kubectl_get_state_json "{{.Namespace}}" "{{.ResourceName}}" "${state_file}" 2>&1 | prepend "get_json_state"
  echo "sleeping..."
  sleep 1
done
