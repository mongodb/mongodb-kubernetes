from typing import List
from pytest import mark, fixture

from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator
from kubetester.kubetester import fixture as yaml_fixture
from kubernetes import client
from kubeobject import CustomObject

from tests.conftest import run_multi_cluster_recovery_tool, MULTI_CLUSTER_OPERATOR_NAME

RESOURCE_NAME = "multi-replica-set"


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: client.ApiClient, namespace: str
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace
    )
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
def test_create_service_entry(service_entry: CustomObject):
    service_entry.create()


@mark.e2e_multi_cluster_recover_network_partition
def test_deploy_operator(multi_cluster_operator_manual_remediation: Operator):
    multi_cluster_operator_manual_remediation.assert_is_running()


@mark.e2e_multi_cluster_recover_network_partition
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.create()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


@mark.e2e_multi_cluster_recover_network_partition
def test_update_service_entry_block_cluster3_traffic(service_entry: CustomObject):
    service_entry.load()
    service_entry["spec"]["hosts"] = [
        "cloud-qa.mongodb.com",
        "api.e2e.cluster1.mongokubernetes.com",
        "api.e2e.cluster2.mongokubernetes.com",
    ]
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
    return_code = run_multi_cluster_recovery_tool(
        member_cluster_names[:-1], namespace, namespace
    )
    assert return_code == 0
    operator = Operator(
        name=MULTI_CLUSTER_OPERATOR_NAME,
        namespace=namespace,
        api_client=central_cluster_client,
    )
    operator._wait_for_operator_ready()
    operator.assert_is_running()


@mark.e2e_multi_cluster_recover_network_partition
def test_mongodb_multi_recovers_removing_cluster(
    mongodb_multi: MongoDBMulti, member_cluster_names: List[str]
):
    mongodb_multi.load()

    mongodb_multi["metadata"]["annotations"]["failedClusters"] = None
    mongodb_multi["spec"]["clusterSpecList"]["clusterSpecs"].pop()
    mongodb_multi.update()
    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=50)

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=800)
