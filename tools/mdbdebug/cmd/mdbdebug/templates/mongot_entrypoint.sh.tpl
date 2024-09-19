#!/usr/bin/env bash

set -Eeou pipefail

{{ template "common.sh.tpl" }}

set -x

container_name="{{.ContainerName}}"

sigterm() {
  exit 0
}
trap sigterm SIGTERM

export LANG=en_GB.UTF-8
tmuxp load -d -y "/scripts/session.yaml"

base_log_dir="{{.BaseLogDir}}"
base_dir="${base_log_dir}/mdb-debug"
container_log_file="${base_log_dir}/container.log"

cr_file="${base_dir}/cr/cr.json"
mongot_config_file="${base_dir}/config/config.json"
pod_file="${base_dir}/pod/pod.json"
sts_file="${base_dir}/sts/sts.json"

mkdir -p "$(dirname "${cr_file}")"
mkdir -p "$(dirname "${pod_file}")"
mkdir -p "$(dirname "${sts_file}")"
mkdir -p "$(dirname "${mongot_config_file}")"

tail_pod_log "{{.Namespace}}" "{{.PodName}}" "${container_name}" "${container_log_file}" &

set +e
while true; do
  kubectl_get_json "{{.Namespace}}" "{{.ResourceType}}" "{{.ResourceName}}" "${cr_file}" 2>&1 | prepend "get_{{.ResourceType}}_cr"

  kubectl get pod -n "{{.Namespace}}" "{{.PodName}}" -o json >${pod_file}.tmp
  mv ${pod_file}.tmp ${pod_file}

  kubectl get sts -n "{{.Namespace}}" "{{.StsName}}" -o json >${sts_file}.tmp
  mv ${sts_file}.tmp ${sts_file}


  get_file "{{.Namespace}}" "{{.PodName}}" "${container_name}" "/mongot/config/config.yml" "${mongot_config_file}.yaml.tmp"
  yq . "${mongot_config_file}.yaml.tmp" -o json >"${mongot_config_file}.tmp"
  mv "${mongot_config_file}.tmp" "${mongot_config_file}"

  sleep 1
done
