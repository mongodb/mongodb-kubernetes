from typing import Dict, List
from pytest import mark, fixture
from kubetester.kubetester import create_testing_namespace
import kubernetes

from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from kubetester.kubetester import fixture as yaml_fixture, skip_if_local


def create_namespace(
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    task_id: str,
    namespace: str,
) -> str:
    for client in member_cluster_clients:
        create_testing_namespace(task_id, namespace, client.api_client)

    return create_testing_namespace(task_id, namespace, central_cluster_client)


@fixture(scope="module")
def mdba_ns(
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_clients: List[MultiClusterClient],
    evergreen_task_id: str,
) -> str:
    return create_namespace(
        central_cluster_client,
        member_cluster_clients,
        evergreen_task_id,
        "mdb-ns-a",
    )


@fixture(scope="module")
def mdbb_ns(evergreen_task_id: str) -> str:
    return create_namespace(
        central_cluster_client,
        member_cluster_clients,
        evergreen_task_id,
        "mdb-ns-a",
    )


@fixture(scope="module")
def mongodb_multi_a(
    central_cluster_client: kubernetes.client.ApiClient, mdba_ns: str
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"), "multi-replica-set", mdba_ns
    )

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource.create()


@fixture(scope="module")
def mongodb_multi_b(
    central_cluster_client: kubernetes.client.ApiClient, mdbb_ns: str
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"), "multi-replica-set", mdbb_ns
    )

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource.create()


@mark.e2e_multi_cluster_clusterwide
def test_deploy_operator(multi_cluster_operator_clustermode: Operator):
    multi_cluster_operator_clustermode.assert_is_running()


@mark.e2e_multi_cluster_clusterwide
def test_create_mongodb_multi_nsa(mongodb_multi_a: MongoDBMulti):
    mongodb_multi_a.assert_reaches_phase(Phase.Running, timeout=700)


@mark.e2e_multi_cluster_clusterwide
def test_create_mongodb_multi_nsb(mongodb_multi_b: MongoDBMulti):
    mongodb_multi_b.assert_reaches_phase(Phase.Running, timeout=700)
