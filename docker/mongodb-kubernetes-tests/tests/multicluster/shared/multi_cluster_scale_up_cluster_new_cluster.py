from typing import Callable, List

import kubernetes
from kubernetes import client
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongotester import with_tls
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.conftest import run_kube_config_creation_tool
from tests.constants import MULTI_CLUSTER_OPERATOR_NAME


def test_deploy_operator(
    install_multi_cluster_operator_set_members_fn: Callable[[List[str]], Operator],
    member_cluster_names: List[str],
    namespace: str,
):
    run_kube_config_creation_tool(member_cluster_names[:-1], namespace, namespace, member_cluster_names)
    # deploy the operator without the final cluster
    operator = install_multi_cluster_operator_set_members_fn(member_cluster_names[:-1])
    operator.assert_is_running()


def test_create_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


def test_statefulsets_have_been_created_correctly(
    mongodb_multi: MongoDBMulti | MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    clients = {c.cluster_name: c for c in member_cluster_clients}

    # read all statefulsets except the last one
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients[:-1])
    cluster_one_client = clients["kind-e2e-cluster-1"]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]
    assert cluster_one_sts.status.ready_replicas == 2

    cluster_two_client = clients["kind-e2e-cluster-2"]
    cluster_two_sts = statefulsets[cluster_two_client.cluster_name]
    assert cluster_two_sts.status.ready_replicas == 1


def test_ops_manager_has_been_updated_correctly_before_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(3)


def test_delete_deployment(namespace: str, central_cluster_client: kubernetes.client.ApiClient):
    client.AppsV1Api(api_client=central_cluster_client).delete_namespaced_deployment(
        MULTI_CLUSTER_OPERATOR_NAME, namespace
    )


def test_re_deploy_operator(
    install_multi_cluster_operator_set_members_fn: Callable[[List[str]], Operator],
    member_cluster_names: List[str],
    namespace: str,
):
    run_kube_config_creation_tool(member_cluster_names, namespace, namespace, member_cluster_names)

    # deploy the operator without all clusters
    operator = install_multi_cluster_operator_set_members_fn(member_cluster_names)
    operator.assert_is_running()


def test_add_new_cluster_to_mongodb_multi_resource(
    mongodb_multi: MongoDBMulti | MongoDB, member_cluster_clients: List[MultiClusterClient]
):
    mongodb_multi.load()
    mongodb_multi["spec"]["clusterSpecList"].append(
        {"members": 2, "clusterName": member_cluster_clients[-1].cluster_name}
    )
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)


def test_statefulsets_have_been_created_correctly_after_cluster_addition(
    mongodb_multi: MongoDBMulti | MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    clients = {c.cluster_name: c for c in member_cluster_clients}
    # read all statefulsets except the last one
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)
    cluster_one_client = clients["kind-e2e-cluster-1"]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]
    assert cluster_one_sts.status.ready_replicas == 2

    cluster_two_client = clients["kind-e2e-cluster-2"]
    cluster_two_sts = statefulsets[cluster_two_client.cluster_name]
    assert cluster_two_sts.status.ready_replicas == 1

    cluster_three_client = clients["kind-e2e-cluster-3"]
    cluster_three_sts = statefulsets[cluster_three_client.cluster_name]
    assert cluster_three_sts.status.ready_replicas == 2


def test_ops_manager_has_been_updated_correctly_after_scaling():
    ac = AutomationConfigTester()
    ac.assert_processes_size(5)


def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti | MongoDB, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(opts=[with_tls(use_tls=True, ca_path=ca_path)])
