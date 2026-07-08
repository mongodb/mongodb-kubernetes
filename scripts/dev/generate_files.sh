#!/usr/bin/env bash
#
# File generation script for pre-commit hooks.
# Each function is invoked as a separate prek hook; scheduling
# (serial dep chain + parallel groups) is handled by prek's priority
# field in .pre-commit-config.yaml, not by this script.
#

set -Eeou pipefail

source scripts/dev/set_env_context.sh
source scripts/funcs/printing

# Per-worktree by default; opt into a shared venv by exporting PROJECT_VENV_PATH.
venv_path="${PROJECT_VENV_PATH:-${PROJECT_DIR}/venv}"
if [ -f "${venv_path}/bin/activate" ]; then
  source "${venv_path}/bin/activate"
fi

mkdir -p "$(go env GOPATH)/bin"

update_mco_tests() {
  echo "Regenerating MCO evergreen tests configuration"
  scripts/dev/run_python.sh scripts/evergreen/e2e/mco/create_mco_tests.py > .evergreen-mco.yml
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
  helm template --namespace mongodb -f helm_chart/values.yaml helm_chart --output-dir "${charttmpdir}" --set operator.installationMethod=yaml "$@"
  cat "${FILES[@]}" >public/mongodb-kubernetes.yaml
  cat "helm_chart/crds/"* >public/crds.yaml

  # generate openshift public example
  rm -rf "${charttmpdir:?}"/*
  helm template --namespace mongodb -f helm_chart/values.yaml helm_chart --output-dir "${charttmpdir}" --values helm_chart/values-openshift.yaml --set operator.installationMethod=yaml "$@"
  cat "${FILES[@]}" >public/mongodb-kubernetes-openshift.yaml

  # generate openshift files for kustomize used for generating OLM bundle
  rm -rf "${charttmpdir:?}"/*
  helm template --namespace mongodb -f helm_chart/values.yaml helm_chart --output-dir "${charttmpdir}" --values helm_chart/values-openshift.yaml \
    --set operator.webhook.registerConfiguration=false --set operator.webhook.installClusterRole=false --set operator.installationMethod=yaml "$@"

  # update kustomize files for OLM bundle with files generated for openshift
  cp "${charttmpdir}/mongodb-kubernetes/templates/operator.yaml" config/manager/manager.yaml
  cp "${charttmpdir}/mongodb-kubernetes/templates/database-roles.yaml" config/rbac/database-roles.yaml
  cp "${charttmpdir}/mongodb-kubernetes/templates/operator-roles-base.yaml" config/rbac/operator-roles-base.yaml
  cp "${charttmpdir}/mongodb-kubernetes/templates/operator-roles-clustermongodbroles.yaml" config/rbac/operator-roles-clustermongodbroles.yaml
  cp "${charttmpdir}/mongodb-kubernetes/templates/operator-roles-pvc-resize.yaml" config/rbac/operator-roles-pvc-resize.yaml
  cp "${charttmpdir}/mongodb-kubernetes/templates/operator-roles-telemetry.yaml" config/rbac/operator-roles-telemetry.yaml

  # generate multi-cluster public example
  rm -rf "${charttmpdir:?}"/*
  helm template --namespace mongodb -f helm_chart/values.yaml helm_chart --output-dir "${charttmpdir}" --values helm_chart/values-multi-cluster.yaml --set operator.installationMethod=yaml "$@"
  cat "${FILES[@]}" >public/mongodb-kubernetes-multi-cluster.yaml
}

generate_manifests() {
  make manifests
}

update_values_yaml_files() {
  # ensure that all helm values files are up to date.
  # shellcheck disable=SC2154
  scripts/dev/run_python.sh scripts/evergreen/release/update_helm_values_files.py
}

update_mongodb_operator_version() {
  # ensure that release.json:mongodbOperator matches calculate_next_version output.
  # MUST run before update_release_json, which propagates this field to dependents.
  scripts/dev/run_python.sh scripts/release/update_mongodb_operator_version.py
}

update_release_json() {
  # ensure that release.json is up 2 date
  # shellcheck disable=SC2154
  scripts/dev/run_python.sh scripts/evergreen/release/update_release.py
}

regenerate_public_rbac_multi_cluster() {
  if [[ "${MDB_REGENERATE_RBAC:-""}" == "true" ]]; then
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

check_kubebuilder_annotations() {
  if grep -r "// kubebuilder" --include="*.go" --exclude-dir=vendor .; then
    echo "Found erroneous kubebuilder annotation"
    return 1
  fi
}

cmd=${1:-""}

if [[ "${cmd}" == "generate_standalone_yaml" ]]; then
  shift 1
  generate_standalone_yaml "$@"
elif [[ "${cmd}" == "update_mco_tests" ]]; then
  update_mco_tests
elif [[ "${cmd}" == "generate_manifests" ]]; then
  generate_manifests
elif [[ "${cmd}" == "update_values" ]]; then
  update_values_yaml_files
elif [[ "${cmd}" == "update_operator_version" ]]; then
  update_mongodb_operator_version
elif [[ "${cmd}" == "update_release" ]]; then
  update_release_json
elif [[ "${cmd}" == "update_licenses" ]]; then
  update_licenses
elif [[ "${cmd}" == "regenerate_public_rbac_multi_cluster" ]]; then
  regenerate_public_rbac_multi_cluster
elif [[ "${cmd}" == "check_kubebuilder_annotations" ]]; then
  check_kubebuilder_annotations
fi
