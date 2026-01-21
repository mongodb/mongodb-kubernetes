#!/usr/bin/env bash

set -Eeou pipefail

set -x

export LANG=en_GB.UTF-8

sigterm() {
    exit 0
}
trap sigterm SIGTERM

tail_file() {
  pod_name=$1
  container_name=$2
  pod_file_path=$3
  log_file_path=$4

  while true; do
    kubectl exec -n "{{.Namespace}}" -c "${container_name}" "{{.PodName}}" -- tail -F "${pod_file_path}" >> "${log_file_path}" || {
      echo "failed to tail ${pod_name}:${pod_file_path} to ${log_file_path}"
      sleep 3
    }
  done
}

tail_pod_log() {
  pod_name=$1
  container_name=$2
  log_file_path=$3

  while true; do
    kubectl logs -n "{{.Namespace}}" -c "${container_name}" "{{.PodName}}" -f  >> "${log_file_path}" || {
      echo "failed to tail logs from ${pod_name} to ${log_file_path}"
      sleep 3
    }
  done
}

kubectl_get_json() {
  resource_type=$1
  resource_name=$2
  file_path=$3

  kubectl get "${resource_type}" "${resource_name}" -n "{{.Namespace}}"  -o json > "${file_path}.tmp" 2>"${file_path}.error.tmp" || {
    echo "{\"error_message\":\"$(cat "${file_path}.error.tmp")\"}" > "${file_path}.tmp"
  }
  mv "${file_path}.tmp" "${file_path}"
}

tmuxp load -d -y "/scripts/session.yaml"

base_dir="/data/logs/mdb-debug"

om_logs_dir="${base_dir}/logs"
om_cr_file="${base_dir}/om/om.json"
om_pod_file="${base_dir}/pod/pod.json"
om_sts_file="${base_dir}/sts/sts.json"

daemon_startup_log_file="daemon-startup.log"
daemon_log_file="daemon.log"
pod_log="pod.log"

mkdir -p "${om_logs_dir}"
mkdir -p "$(dirname "${om_cr_file}")"
mkdir -p "$(dirname "${om_pod_file}")"
mkdir -p "$(dirname "${om_sts_file}")"

tail_pod_log "{{.PodName}}" "mongodb-backup-daemon" "${om_logs_dir}/${pod_log}" &
tail_file "{{.PodName}}" "mongodb-backup-daemon" "/mongodb-ops-manager/logs/${daemon_startup_log_file}"  "${om_logs_dir}/${daemon_startup_log_file}" &
tail_file "{{.PodName}}" "mongodb-backup-daemon" "/mongodb-ops-manager/logs/${daemon_log_file}"     "${om_logs_dir}/${daemon_log_file}" &

set +e
while true; do
    kubectl_get_json "om" "{{.ResourceName}}" "${om_cr_file}"
    kubectl_get_json "pod" "{{.PodName}}" "${om_pod_file}"
    kubectl_get_json "sts" "{{.StsName}}" "${om_sts_file}"

    sleep 1
done
