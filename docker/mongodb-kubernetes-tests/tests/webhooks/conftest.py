import subprocess

from kubernetes.client.rest import ApiException
from kubetester import read_configmap
from kubetester.kubetester import wait_for_operator_pod_present, wait_for_operator_pod_restart
from kubetester.operator import Operator
from pytest import fixture
from tests.conftest import (
    generate_and_apply_member_registration,
    generate_and_apply_member_resources,
    get_default_operator,
    local_operator,
)


# ClusterMongoDBRole support is opt-in: the operator reconciles ClusterMongoDBRoles only once its own
# cluster is registered as a MemberCluster and its member ServiceAccount there holds the RBAC. These
# tests run only in single-cluster variants, so opt the only cluster in via the day-2 flow.
@fixture(scope="module")
def default_operator(
    namespace: str,
    operator_installation_config: dict[str, str],
    operator_config_extra_spec: dict,
) -> Operator:
    operator = get_default_operator(
        namespace, operator_installation_config, operator_config_extra_spec=operator_config_extra_spec
    )

    # Member and central cluster names double as kubeconfig context names in the E2E harness; in
    # single-cluster runs the only cluster is the current context.
    cluster = subprocess.check_output(["kubectl", "config", "current-context"], text=True).strip()

    # The operator restarts when the identity ConfigMap first appears, so expect a restart only when
    # it is absent: an earlier task in the task group may have applied it already.
    restart_count = None
    if not local_operator():
        operator_name = operator.name or "mongodb-kubernetes-operator"
        try:
            read_configmap(namespace, "mck-member-cluster")
        except ApiException as e:
            if e.status != 404:
                raise
            restart_count = wait_for_operator_pod_present(namespace, operator_name)

    generate_and_apply_member_resources([cluster], namespace, operator_cluster_mongodb_role=True)
    generate_and_apply_member_registration([cluster], namespace, cluster)

    if restart_count is not None:
        wait_for_operator_pod_restart(namespace, operator_name, restart_count)
        operator.wait_for_operator_ready()
        operator.wait_for_operator_webhook_ready()

    return operator


def pytest_runtest_setup(item):
    """This allows to automatically install the default Operator before running any test"""
    if "default_operator" not in item.fixturenames:
        item.fixturenames.insert(0, "default_operator")
