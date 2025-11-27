from typing import List, Optional

import kubernetes
from kubeobject import CustomObject
from kubernetes import client
from kubetester import delete_statefulset, statefulset_is_deleted
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.kubetester import KubernetesTester, run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.conftest import get_member_cluster_api_client
from tests.multicluster.conftest import (
    create_service_entries_objects,
)

FAILED_MEMBER_CLUSTER_NAME = "kind-e2e-cluster-3"


def test_label_namespace(namespace: str, central_cluster_client: kubernetes.client.ApiClient):
    api = client.CoreV1Api(api_client=central_cluster_client)

    labels = {"istio-injection": "enabled"}
    ns = api.read_namespace(name=namespace)

    ns.metadata.labels.update(labels)
    api.replace_namespace(name=namespace, body=ns)


def test_create_service_entry(service_entries: List[CustomObject]):
    for service_entry in service_entries:
        service_entry.update()


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_create_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


def test_update_service_entry_block_failed_cluster_traffic(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
):
    healthy_cluster_names = [
        cluster_name for cluster_name in member_cluster_names if cluster_name != FAILED_MEMBER_CLUSTER_NAME
    ]
    service_entries = create_service_entries_objects(namespace, central_cluster_client, healthy_cluster_names)
    for service_entry in service_entries:
        print(f"service_entry={service_entries}")
        service_entry.update()


def test_mongodb_multi_leaves_running_state(
    mongodb_multi: MongoDBMulti | MongoDB,
):
    mongodb_multi.load()
    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=300)


def test_delete_database_statefulset_in_failed_cluster(
    mongodb_multi: MongoDBMulti | MongoDB, member_cluster_names: list[str]
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


def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti | MongoDB):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


def test_replica_reaches_running(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.load()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


def test_number_numbers_in_ac(mongodb_multi: MongoDBMulti | MongoDB):
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    desiredmembers = 0
    for c in mongodb_multi["spec"]["clusterSpecList"]:
        desiredmembers += c["members"]

    processes = tester.get_replica_set_processes(mongodb_multi.name)
    assert len(processes) == desiredmembers


def test_sts_count_in_member_cluster(
    mongodb_multi: MongoDBMulti | MongoDB,
    member_cluster_names: list[str],
    member_cluster_clients: List[MultiClusterClient],
):
    failed_cluster_idx = member_cluster_names.index(FAILED_MEMBER_CLUSTER_NAME)
    clients = member_cluster_clients[:]
    clients.pop(failed_cluster_idx)
    # assert the distribution of member cluster3 nodes.

    statefulsets = mongodb_multi.read_statefulsets(clients)
    cluster_one_client = clients[0]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]
    assert cluster_one_sts.status.ready_replicas == 3

    cluster_two_client = clients[1]
    cluster_two_sts = statefulsets[cluster_two_client.cluster_name]
    assert cluster_two_sts.status.ready_replicas == 2
