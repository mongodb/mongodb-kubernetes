from typing import List

import kubernetes
from kubeobject import CustomObject
from kubernetes import client
from kubetester import delete_statefulset, statefulset_is_deleted
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import (
    MULTI_CLUSTER_OPERATOR_NAME,
    get_member_cluster_api_client,
    run_multi_cluster_recovery_tool,
)
from tests.multicluster.conftest import (
    cluster_spec_list,
    create_service_entries_objects,
)

FAILED_MEMBER_CLUSTER_NAME = "kind-e2e-cluster-3"
RESOURCE_NAME = "multi-replica-set"


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi.yaml"), RESOURCE_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
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
        service_entry.update()


@mark.e2e_multi_cluster_recover_network_partition
def test_deploy_operator(multi_cluster_operator_manual_remediation: Operator):
    multi_cluster_operator_manual_remediation.assert_is_running()


@mark.e2e_multi_cluster_recover_network_partition
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


@mark.e2e_multi_cluster_recover_network_partition
def test_update_service_entry_block_failed_cluster_traffic(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
):
    healthy_cluster_names = [
        cluster_name for cluster_name in member_cluster_names if cluster_name != FAILED_MEMBER_CLUSTER_NAME
    ]
    service_entries = create_service_entries_objects(
        namespace,
        central_cluster_client,
        healthy_cluster_names,
    )
    for service_entry in service_entries:
        print(f"service_entry={service_entries}")
        service_entry.update()


@mark.e2e_multi_cluster_recover_network_partition
def test_delete_database_statefulset_in_failed_cluster(
    mongodb_multi: MongoDBMulti,
    member_cluster_names: list[str],
):
    failed_cluster_idx = member_cluster_names.index(FAILED_MEMBER_CLUSTER_NAME)
    sts_name = f"{mongodb_multi.name}-{failed_cluster_idx}"
    try:
        delete_statefulset(
            mongodb_multi.namespace,
            sts_name,
            propagation_policy="Background",
            api_client=get_member_cluster_api_client(FAILED_MEMBER_CLUSTER_NAME),
        )
    except kubernetes.client.ApiException as e:
        if e.status != 404:
            raise e

    run_periodically(
        lambda: statefulset_is_deleted(
            mongodb_multi.namespace,
            sts_name,
            api_client=get_member_cluster_api_client(FAILED_MEMBER_CLUSTER_NAME),
        ),
        timeout=120,
    )


@mark.e2e_multi_cluster_recover_network_partition
def test_mongodb_multi_enters_failed_state(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    central_cluster_client: client.ApiClient,
):
    mongodb_multi.load()
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

    last_transition_time = mongodb_multi.get_status_last_transition_time()

    mongodb_multi["metadata"]["annotations"]["failedClusters"] = None
    mongodb_multi["spec"]["clusterSpecList"].pop()
    mongodb_multi.update()
    mongodb_multi.assert_state_transition_happens(last_transition_time)

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1500)
