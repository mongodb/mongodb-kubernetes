#!/usr/bin/env bash

# This script is for attaching to a previously created debugging pod.

set -Eeou pipefail

if ! which fzf &>/dev/null ; then
  echo "you need to install fzf:"
  echo "  brew install fzf"
  exit 1
fi

cmd_file="${PROJECT_DIR}/.generated/mdb-debug.attach"

parse_json_to_map() {
    local json="$1"
    declare -n map_ref="$2"

    while IFS=$'\t' read -r key value; do
        # shellcheck disable=SC2034
        map_ref["${key}"]="${value}"
    done < <(echo "${json}" | jq -r 'to_entries | .[] | "\(.key)\t\(.value)"')
}

print_map() {
    declare -n map_ref="$1"
    for key in "${!map_ref[@]}"; do
        echo "${key}: ${map_ref[$key]}"
    done
}

attach() {
  commands="$(jq -r '.[] | "\(.shortName): debug pod \(.namespace)/\(.podName) (will attach to \(.debugPodName))"' < "${cmd_file}")"
  short_name=$(echo "${commands}" | fzf -n 1 -d ':' | cut -d ':' -f1)
  echo "Picked pod to debug: ${short_name}"

  attach_json="$(jq -r ".[] | select(.shortName == \"${short_name}\")" < "${cmd_file}")"
  declare -A attach_map
  parse_json_to_map "${attach_json}" attach_map
  echo "Details of the selected attach json: "
  print_map attach_map
  debug_sts_name="${attach_map["debugStsName"]}"
  debug_pod_name="${attach_map["debugPodName"]}"
  debug_pod_context="${attach_map["debugPodContext"]}"
  if [[ "${debug_pod_context}" == "__default" ]]; then
    debug_pod_context="$(kubectl config current-context)"
  fi
  cmd="${attach_map["command"]}"
  namespace="${attach_map["namespace"]}"

  if [[ "${cmd}" == "" ]]; then
    echo "Couldn't find attach command from the listed below:"
    cat "${cmd_file}"
    echo
    exit 1
  fi

  echo "Scaling statefulset $ to 1 replicas"
  kubectl --context "${debug_pod_context}" --namespace "${namespace}" scale statefulsets "${debug_sts_name}" --replicas=1
  kubectl --context "${debug_pod_context}" --namespace "${namespace}" rollout status statefulset "${debug_sts_name}" --timeout=60s
  kubectl --context "${debug_pod_context}" --namespace "${namespace}" -it exec "${debug_pod_name}" -- tmux attach
}

pick_deployment() {
  configmaps=()
  while IFS= read -r cm; do
    configmaps+=("${cm}")
  done < <(kubectl get configmaps --namespace "${NAMESPACE}" -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | grep "attach-commands")

  config_map_to_deployment_name_regex='s/mdb-debug-attach-commands-\([a-z0-9.-]*\)/\1/g'
  if [[ ${#configmaps[@]} == 0 ]]; then
    echo "No attach commands config maps found!" >&2
  elif [[ ${#configmaps[@]} == 1 ]]; then
    echo "Found one config map with attach commands: ${configmaps[0]}" >&2
    echo -n "${configmaps[0]}" | sed "${config_map_to_deployment_name_regex}"
  else
    echo "Found multiple config maps with attach commands: ${configmaps[*]}" >&2
    deployments=$(echo -n "${configmaps[*]}" | tr ' ' '\n' | sed "${config_map_to_deployment_name_regex}")
    picked_deployment=$(
      fzf -d ' ' --header-first --layout=reverse --header "Pick deployment first:" <<< "${deployments}" \
      --preview "kubectl get cm mdb-debug-attach-commands-{} --namespace ${NAMESPACE} -o jsonpath='{.data.attachCommands}'" \
    )
    if [[ ${picked_deployment} != "" ]]; then
      echo "${picked_deployment}"
    fi
  fi
}


# Function to retry a command with a configurable delay
# Usage: retry_cmd "command to execute" delay_seconds
retry_cmd() {
    local cmd="$1"
    local delay="${2:-3}"

    while true; do
        eval "$cmd" || true
        echo "Retrying..."
        sleep "$delay"
    done
}

deployment=$(pick_deployment)
if [[ ${deployment} == "" ]]; then
  echo "No deployment picked. Exiting."
  exit 1
fi

echo "Picked deployment: ${deployment}"
kubectl get cm "mdb-debug-attach-commands-${deployment}" --namespace "${NAMESPACE}" -o jsonpath='{.data.commands}' >"${cmd_file}"
retry_cmd attach
