export LANG=en_GB.UTF-8


sigterm() {
  exit 0
}

trap sigterm SIGTERM

tail_file() {
  namespace=$1
  pod_name=$2
  container_name=$3
  pod_file_path=$4
  log_file_path=$5

  while true; do
    kubectl exec -n "${namespace}" -c "${container_name}" "${pod_name}" -- tail -F "${pod_file_path}" >>"${log_file_path}" || {
      echo "failed to tail ${pod_name}:${pod_file_path} to ${log_file_path}"
      sleep 3
    }
  done
}

exec_cmd() {
  namespace=$1
  pod_name=$2
  container_name=$3
  cmd=$4

  # shellcheck disable=SC2086
  kubectl exec -n "${namespace}" -c "${container_name}" "${pod_name}" -- bash -c "${cmd}" || {
    echo "failed to exec in ${pod_name} cmd: ${cmd}"
  }
}

get_file() {
  namespace=$1
  pod_name=$2
  container_name=$3
  src_file_path=$4
  dst_file_path=$5

  kubectl exec -n "${namespace}" "${pod_name}" -c "${container_name}" -- cat "${src_file_path}" > "${dst_file_path}" || {
    echo "failed to get file ${pod_name}:${src_file_path} to ${dst_file_path}"
    return 1
  }
  return 0
}

tail_pod_log() {
  namespace=$1
  pod_name=$2
  container_name=$3
  log_file_path=$4

  while true; do
    kubectl logs -n "${namespace}" -c "${container_name}" "${pod_name}" --tail=0 -f >>"${log_file_path}" || {
      echo "failed to tail logs from ${pod_name} to ${log_file_path}"
      sleep 3
    }
  done
}

kubectl_get_json() {
  namespace=$1
  resource_type=$2
  resource_name=$3
  file_path=$4

  kubectl get "${resource_type}" "${resource_name}" -n "${namespace}" -o json >"${file_path}.tmp" 2>"${file_path}.error.tmp" || {
    echo "{\"error_message\":\"$(cat "${file_path}.error.tmp")\"}" >"${file_path}.tmp"
  }
  mv "${file_path}.tmp" "${file_path}"
}

kubectl_get_state_json() {
  namespace=$1
  resource_name=$2
  file_path=$3

  kubectl get cm "${resource_name}-state" -n "${namespace}" -o json | jq -r '.data.state' | jq . >"${file_path}.tmp" 2>"${file_path}.error.tmp" || {
    echo "{\"error_message\":\"$(cat "${file_path}.error.tmp")\"}" >"${file_path}.tmp"
  }
  mv "${file_path}.tmp" "${file_path}"
}

get_om_creds() {
  namespace=$1
  secret=$2

  kubectl get secret "${secret}" -n "${namespace}" -o json | jq -r '.data | with_entries(.value |= @base64d) | if .user then "\(.user):\(.publicApiKey)" else "\(.publicKey):\(.privateKey)" end'
}

get_ac() {
  namespace=$1
  base_url=$2
  project_id=$3
  agent_api_key=$4

  curl -s -k -u "${project_id}:${agent_api_key}" "${base_url}/agents/api/automation/conf/v1/${project_id}?debug=true" 2>/dev/null | jq .
}

get_project_id() {
  base_url=$1
  om_creds=$2
  project_name=$3

  curl -s -k -u "${om_creds}" --digest "${base_url}/api/public/v1.0/groups/byName/${project_name}" 2>/dev/null | jq -r .id
}

get_project_data() {
  namespace=$1
  resource=$2

  resource_json=$(kubectl get -n "${namespace}" mdb "${resource}" -o json)
  project_configmap=$(jq -r 'if .spec.cloudManager then .spec.cloudManager.configMapRef.name else .spec.opsManager.configMapRef.name end' <<<"${resource_json}")

  creds_secret=$(jq -r '.spec.credentials' <<<"${resource_json}")
  om_creds=$(get_om_creds "${namespace}" "${creds_secret}")

  project_configmap_json=$(kubectl get configmap "${project_configmap}" -n "${namespace}" -o json)
  org_id=$(jq -r '.data.orgId' <<<"${project_configmap_json}")
  project_name=$(jq -r '.data.projectName' <<<"${project_configmap_json}")
  base_url=$(jq -r '.data.baseUrl' <<<"${project_configmap_json}")

  project_id=$(get_project_id "${base_url}" "${om_creds}" "${project_name}")

  group_secret_json=$(kubectl get -n "${namespace}" secret "${project_id}-group-secret" -o json)
  agent_api_key=$(jq -r '.data | with_entries(.value |= @base64d) | .agentApiKey' <<<"${group_secret_json}")

  echo "${org_id}|${project_name}|${base_url}|${project_id}|${agent_api_key}"
}

get_project_data2() {
  namespace=$1
  resource_name=$2
  pod_name=$3

  project_id=$(kubectl get -n "${namespace}" pod "${pod_name}" -o json | jq -r '.spec.containers[] | select(.name == "mongodb-enterprise-database" or .name == "mongodb-agent").env[] | select(.name == "GROUP_ID") | .value' | head -n 1)
  base_url=$(kubectl get -n "${namespace}" pod "${pod_name}" -o json | jq -r '.spec.containers[] | select(.name == "mongodb-enterprise-database" or .name == "mongodb-agent").env[] | select(.name == "BASE_URL") | .value' | head -n 1)
  group_secret_json=$(kubectl get -n "${namespace}" secret "${project_id}-group-secret" -o json)
  agent_api_key=$(jq -r '.data | with_entries(.value |= @base64d) | .agentApiKey' <<<"${group_secret_json}")

  echo "${org_id}|${base_url}|${project_id}|${agent_api_key}"
}

trim_config() {
  cfg_file=$1
  jq '. | del(.mongoshVersion, .mongoDbVersions, .mongoDbToolsVersion, .clientPIT)' <"${cfg_file}"
}

prepend() {
  prefix=$1
  awk -v prefix="${prefix}" '{printf "%s: %s\n", prefix, $0}'
}
