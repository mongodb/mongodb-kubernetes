#!/usr/bin/env bash
#
# File generation script for pre-commit hooks.
# This script generates various files (YAML configs, manifests, etc.).
#

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/printing

if [ -f "${PROJECT_DIR}/venv/bin/activate" ]; then
  source "${PROJECT_DIR}/venv/bin/activate"
fi

mkdir -p "$(go env GOPATH)/bin"

update_mco_tests() {
  echo "Regenerating MCO evergreen tests configuration"
  python scripts/evergreen/e2e/mco/create_mco_tests.py >.evergreen-mco.yml
}

# Generates a yaml file to install the operator from the helm sources.
generate_standalone_yaml() {
  charttmpdir=$(mktemp -d 2>/dev/null || mktemp -d -t 'charttmpdir')
  charttmpdir=${charttmpdir}/chart
  mkdir -p "${charttmpdir}"

  FILES=(
    "${charttmpdir}/mongodb-kubernetes/templates/operator-roles-base.yaml"
    "${charttmpdir}/mongodb-kubernetes/templates/operator-roles-clustermongodbroles.yaml"
    "${charttmpdir}/mongodb-kubernetes/templates/operator-roles-pvc-resize.yaml"
    "${charttmpdir}/mongodb-kubernetes/templates/operator-roles-telemetry.yaml"
    "${charttmpdir}/mongodb-kubernetes/templates/operator-roles-webhook.yaml"
    "${charttmpdir}/mongodb-kubernetes/templates/database-roles.yaml"
    "${charttmpdir}/mongodb-kubernetes/templates/operator-sa.yaml"
    "${charttmpdir}/mongodb-kubernetes/templates/operator.yaml"
  )

  # generate normal public example
  helm template --namespace mongodb -f helm_chart/values.yaml helm_chart --output-dir "${charttmpdir}" "$@"
  cat "${FILES[@]}" >public/mongodb-kubernetes.yaml
  cat "helm_chart/crds/"* >public/crds.yaml

  # generate openshift public example
  rm -rf "${charttmpdir:?}"/*
  helm template --namespace mongodb -f helm_chart/values.yaml helm_chart --output-dir "${charttmpdir}" --values helm_chart/values-openshift.yaml "$@"
  cat "${FILES[@]}" >public/mongodb-kubernetes-openshift.yaml

  # generate openshift files for kustomize used for generating OLM bundle
  rm -rf "${charttmpdir:?}"/*
  helm template --namespace mongodb -f helm_chart/values.yaml helm_chart --output-dir "${charttmpdir}" --values helm_chart/values-openshift.yaml \
    --set operator.webhook.registerConfiguration=false --set operator.webhook.installClusterRole=false "$@"

  # update kustomize files for OLM bundle with files generated for openshift
  cp "${charttmpdir}/mongodb-kubernetes/templates/operator.yaml" config/manager/manager.yaml
  cp "${charttmpdir}/mongodb-kubernetes/templates/database-roles.yaml" config/rbac/database-roles.yaml
  cp "${charttmpdir}/mongodb-kubernetes/templates/operator-roles-base.yaml" config/rbac/operator-roles-base.yaml
  cp "${charttmpdir}/mongodb-kubernetes/templates/operator-roles-clustermongodbroles.yaml" config/rbac/operator-roles-clustermongodbroles.yaml
  cp "${charttmpdir}/mongodb-kubernetes/templates/operator-roles-pvc-resize.yaml" config/rbac/operator-roles-pvc-resize.yaml
  cp "${charttmpdir}/mongodb-kubernetes/templates/operator-roles-telemetry.yaml" config/rbac/operator-roles-telemetry.yaml

  # generate multi-cluster public example
  rm -rf "${charttmpdir:?}"/*
  helm template --namespace mongodb -f helm_chart/values.yaml helm_chart --output-dir "${charttmpdir}" --values helm_chart/values-multi-cluster.yaml "$@"
  cat "${FILES[@]}" >public/mongodb-kubernetes-multi-cluster.yaml
}

generate_manifests() {
  make manifests
}

update_values_yaml_files() {
  # ensure that all helm values files are up to date.
  # shellcheck disable=SC2154
  python scripts/evergreen/release/update_helm_values_files.py
}

update_release_json() {
  # ensure that release.json is up 2 date
  # shellcheck disable=SC2154
  python scripts/evergreen/release/update_release.py
}

regenerate_public_rbac_multi_cluster() {
  if [[ -z "${EVERGREEN_MODE:-}" ]]; then
    # According to the latest SSDLC recommendations, the CI needs to always check all the files. Not just delta.
    git_last_changed=$(git ls-tree -r origin/master --name-only)
  else
    git_last_changed=$(git diff --cached --name-only --diff-filter=ACM origin/master)
  fi

  if echo "${git_last_changed}" | grep -q -e 'cmd/kubectl-mongodb' -e 'pkg/kubectl-mongodb'; then
    echo 'regenerating multicluster RBAC public example'
    pushd pkg/kubectl-mongodb/common/
    EXPORT_RBAC_SAMPLES="true" go test ./... -run TestPrintingOutRolesServiceAccountsAndRoleBindings
    popd
  fi
}

update_licenses() {
  if [[ "${MDB_UPDATE_LICENSES:-""}" == "true" ]]; then
    echo 'regenerating licenses'
    time scripts/evergreen/update_licenses.sh 2>&1 | prepend "update_licenses"
  fi
}

# bg_job_ vars are global; run_job_in_background function is appending to them on each call
bg_job_pids=()
bg_job_pids_with_names=()

get_job_name() {
  local search_pid="$1"
  local match
  match=$(printf '%s\n' "${bg_job_pids_with_names[@]}" | grep "^${search_pid}:")
  echo "${match#*:}" # Remove everything up to and including the colon
}

# Executes function given on the first argument as background job.
# It's ensuring logs are properly prefixed by the name and
# the job's pid is captured in bg_jobs array in order to wait for completion.
run_job_in_background() {
  job_name=$1
  time ${job_name} 2>&1 | prepend "${job_name}" &

  local job_pid=$!
  bg_job_pids+=("${job_pid}")
  bg_job_pids_with_names+=("${job_pid}:${job_name}")
  echo "Started ${job_name} with PID: ${job_pid}"
}

# Waits for all background jobs stored in bg_job_pids and check their exit codes.
wait_for_all_background_jobs() {
  failures=()
  for pid in "${bg_job_pids[@]}"; do
    wait "${pid}" || {
      job_name=$(get_job_name "${pid}")
      failures+=("    ${RED}${job_name} (PID ${pid})${NO_COLOR}")
    }
  done

  if [[ ${#failures[@]} -gt 0 ]]; then
    echo -e "${RED}Some generation jobs have failed:${NO_COLOR}"
    for failure in "${failures[@]}"; do
      echo -e "${failure}"
    done
    return 1
  fi

  return 0
}

check_kubebuilder_annotations() {
  if grep -r "// kubebuilder" --include="*.go" --exclude-dir=vendor .; then
    echo "Found erroneous kubebuilder annotation"
    return 1
  fi
}

generate_all() {
  title "Running pre-commit jobs in parallel"

  # NOTE: The following are now separate pre-commit hooks that run serially
  # BEFORE this hook to avoid race conditions with release.json:
  #   - update_release_json (writes release.json)
  #   - update_values_yaml_files (reads release.json)
  #   - generate_standalone_yaml (reads values files)

  # All remaining jobs can run in parallel
  run_job_in_background "generate_manifests"
  run_job_in_background "update_mco_tests"
  run_job_in_background "regenerate_public_rbac_multi_cluster"
  run_job_in_background "update_licenses"
  run_job_in_background "check_kubebuilder_annotations"

  # Wait for all jobs
  if ! wait_for_all_background_jobs; then
    return 1
  fi

  echo -e "${GREEN}All generation jobs completed successfully!${NO_COLOR}"
}

cmd=${1:-"generate_all"}

if [[ "${cmd}" == "generate_standalone_yaml" ]]; then
  shift 1
  generate_standalone_yaml "$@"
elif [[ "${cmd}" == "generate_all" ]]; then
  time generate_all
elif [[ "${cmd}" == "update_mco_tests" ]]; then
  update_mco_tests
elif [[ "${cmd}" == "generate_manifests" ]]; then
  generate_manifests
elif [[ "${cmd}" == "update_values" ]]; then
  update_values_yaml_files
elif [[ "${cmd}" == "update_release" ]]; then
  update_release_json
elif [[ "${cmd}" == "update_licenses" ]]; then
  update_licenses
fi
