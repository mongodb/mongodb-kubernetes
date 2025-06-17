#!/usr/bin/env bash
set -Eeou pipefail -o posix

start_time=$(date +%s)

source scripts/funcs/checks
source scripts/funcs/kubernetes
source scripts/funcs/printing
source scripts/evergreen/e2e/dump_diagnostic_information.sh
source scripts/evergreen/e2e/lib.sh
source scripts/dev/set_env_context.sh

run_e2e_mco_tests() {
  local cluster_wide="false"

  if [[ "${TEST_NAME}" == "replica_set_cross_namespace_deploy" ]]; then
    cluster_wide="true"
  fi

  # mco pv test relies on this
  docker exec kind-control-plane mkdir -p /opt/data/mongo-data-{0..2} /opt/data/mongo-logs-{0..2}

  set +e # let's not fail here, such that we can still dump all information
  scripts/evergreen/run_python.sh mongodb-community-operator/scripts/dev/e2e.py --test "${TEST_NAME}" --distro ubi --cluster-wide "${cluster_wide}"
  local test_results=$?
  set -e

  return ${test_results}
}

if [[ -n "${KUBECONFIG:-}" && ! -f "${KUBECONFIG}" ]]; then
  echo "Kube configuration: ${KUBECONFIG} file does not exist!"
  exit 1
fi

#
# This is the main entry point for running e2e tests. It can be used both for simple e2e tests (running a single test
# application) and for the Operator upgrade ones involving two steps (deploy previous Operator version, run test, deploy
# a new Operator - run verification tests)
# All the preparation work (fetching OM information, configuring resources) is done before running tests but
# it should be moved to e2e tests themselves (TODO)
#
check_env_var "TASK_NAME" "The 'TASK_NAME' must be specified for the Operator e2e tests"

# 1. Ensure the namespace exists - it should be created during the private-context switch

current_context=$(kubectl config current-context)
# shellcheck disable=SC2154
if [[ "${KUBE_ENVIRONMENT_NAME}" == "multi" ]]; then
  current_context="${CENTRAL_CLUSTER}"
  kubectl config set-context "${current_context}" "--namespace=${NAMESPACE}" &>/dev/null || true
  kubectl config use-context "${current_context}"
  echo "Current context: ${current_context}, namespace=${NAMESPACE}"
  kubectl get nodes | grep "control-plane"
fi

ensure_namespace "${NAMESPACE}"

# 2. Fetch OM connection information - it will be saved to environment variables
. scripts/evergreen/e2e/fetch_om_information.sh

# 3. Configure Operator resources
. scripts/evergreen/e2e/configure_operator.sh

if [[ "${RUNNING_IN_EVG:-false}" == "true" ]]; then
  # 4. install honeycomb observability
  . scripts/evergreen/e2e/performance/honeycomb/install-hc.sh
fi

if [ -n "${TEST_NAME_OVERRIDE:-}" ]; then
  echo "Running test with override: ${TEST_NAME_OVERRIDE}"
  TEST_NAME="${TEST_NAME_OVERRIDE}"
else
  TEST_NAME="${TASK_NAME:?}"
fi

export TEST_NAME
echo "TEST_NAME is set to: ${TEST_NAME}"

delete_operator "${NAMESPACE}"

# We'll have the task running for the alloca  ted time, minus the time it took us
# to get all the way here, assuming configuring and deploying the operator can
# take a bit of time. This is needed because Evergreen kills the process *AND*
# Docker containers running on the host when it hits a timeout. Under these
# circumstances and in Kind based environments, it is impossible to fetch the
# results from the Kubernetes cluster running on top of Docker.
#
current_time=$(date +%s)
elapsed_time=$((current_time - start_time))

task_timeout=$(get_timeout_for_task "${TASK_NAME:?}")

timeout_sec=$((task_timeout - elapsed_time - 60))
echo "This task is allowed to run for ${timeout_sec} seconds"
TEST_RESULTS=0

# 4. Main test run.
if [[ "${BUILD_VARIANT:-${CURRENT_VARIANT_CONTEXT}}" == "e2e_mco_tests" ]]; then
  run_e2e_mco_tests || TEST_RESULTS=$?
else
  timeout --foreground "${timeout_sec}" scripts/evergreen/e2e/single_e2e.sh || TEST_RESULTS=$?
fi

# Dump information from all clusters.
# TODO: ensure cluster name is included in log files so there is no overwriting of cross cluster files.
# shellcheck disable=SC2154
if [[ "${KUBE_ENVIRONMENT_NAME:-}" = "multi" ]]; then
  echo "Dumping diagnostics for context ${CENTRAL_CLUSTER}"
  dump_all "${CENTRAL_CLUSTER}" || true

  for member_cluster in ${MEMBER_CLUSTERS}; do
    echo "Dumping diagnostics for context ${member_cluster}"
    dump_all "${member_cluster}" || true
  done
else
  # Dump all the information we can from this namespace
  dump_all || true
fi

# we only have static cluster in openshift, otherwise there is no need to mark and clean them up here
if [[ ${CLUSTER_TYPE} == "openshift" ]]; then
  if [[ "${TEST_RESULTS}" -ne 0 ]]; then
    # Mark namespace as failed to be cleaned later
    kubectl label "namespace/${NAMESPACE}" "evg/state=failed" --overwrite=true

    if [ "${ALWAYS_REMOVE_TESTING_NAMESPACE-}" = "true" ]; then
      # Failed namespaces might cascade into more failures if the namespaces
      # are not being removed soon enough.
      reset_namespace "$(kubectl config current-context)" "${NAMESPACE}" || true
    fi
  else
    if [[ "${KUBE_ENVIRONMENT_NAME}" = "multi" ]]; then
      echo "Tearing down cluster ${CENTRAL_CLUSTER}"
      reset_namespace "${CENTRAL_CLUSTER}" "${NAMESPACE}" || true

      for member_cluster in ${MEMBER_CLUSTERS}; do
        echo "Tearing down cluster ${member_cluster}"
        reset_namespace "${member_cluster}" "${NAMESPACE}" || true
      done
    else
      # If the test pass, then the namespace is removed
      reset_namespace "$(kubectl config current-context)" "${NAMESPACE}" || true
    fi
  fi
fi

# We exit with the test result to surface status to Evergreen.
exit ${TEST_RESULTS}
