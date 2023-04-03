from typing import List
from pytest import mark, fixture

import kubernetes
from kubetester import create_or_update
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator
from kubetester.kubetester import fixture as yaml_fixture
from kubernetes import client
from kubeobject import CustomObject

from tests.conftest import run_multi_cluster_recovery_tool, MULTI_CLUSTER_OPERATOR_NAME
from .conftest import create_service_entries_objects

RESOURCE_NAME = "multi-replica-set"


@fixture(scope="module")
def mongodb_multi(central_cluster_client: client.ApiClient, namespace: str) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace)
    resource["spec"]["persistent"] = False
    resource.api = client.CustomObjectsApi(central_cluster_client)

    return resource


@mark.e2e_multi_cluster_recover_network_partition
def test_label_namespace(namespace: str, central_cluster_client: client.ApiClient):

    api = client.CoreV1Api(api_client=central_cluster_client)

    labels = {"istio-injection": "enabled"}
    ns = api.read_namespace(name=namespace)

    ns.metadata.labels.update(labels)
    api.replace_namespace(name=namespace, body=ns)


@mark.e2e_multi_cluster_recover_network_partition
def test_create_service_entry(service_entries: List[CustomObject]):
    for service_entry in service_entries:
        create_or_update(service_entry)


@mark.e2e_multi_cluster_recover_network_partition
def test_deploy_operator(multi_cluster_operator_manual_remediation: Operator):
    multi_cluster_operator_manual_remediation.assert_is_running()


@mark.e2e_multi_cluster_recover_network_partition
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.create()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


@mark.e2e_multi_cluster_recover_network_partition
def test_update_service_entry_block_cluster3_traffic(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
):

    service_entries = create_service_entries_objects(
        namespace,
        central_cluster_client,
        [member_cluster_names[0], member_cluster_names[1]],
    )
    for service_entry in service_entries:
        print(f"service_entry={service_entries}")
        service_entry.update()


@mark.e2e_multi_cluster_recover_network_partition
def test_mongodb_multi_enters_failed_state(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    central_cluster_client: client.ApiClient,
):
    mongodb_multi.load()
    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=50)
    mongodb_multi.assert_reaches_phase(Phase.Failed, timeout=100)


@mark.e2e_multi_cluster_recover_network_partition
def test_recover_operator_remove_cluster(
    member_cluster_names: List[str],
    namespace: str,
    central_cluster_client: client.ApiClient,
):
    return_code = run_multi_cluster_recovery_tool(member_cluster_names[:-1], namespace, namespace)
    assert return_code == 0
    operator = Operator(
        name=MULTI_CLUSTER_OPERATOR_NAME,
        namespace=namespace,
        api_client=central_cluster_client,
    )
    operator._wait_for_operator_ready()
    operator.assert_is_running()


@mark.e2e_multi_cluster_recover_network_partition
def test_mongodb_multi_recovers_removing_cluster(mongodb_multi: MongoDBMulti, member_cluster_names: List[str]):
    mongodb_multi.load()

    mongodb_multi["metadata"]["annotations"]["failedClusters"] = None
    mongodb_multi["spec"]["clusterSpecList"].pop()
    mongodb_multi.update()
    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=50)

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1500)
