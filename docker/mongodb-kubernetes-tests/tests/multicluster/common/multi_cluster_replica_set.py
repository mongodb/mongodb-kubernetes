from typing import Dict, List

import kubernetes
import pytest
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import delete_statefulset, get_statefulset, wait_until
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.conftest import (
    assert_log_rotation_process,
    member_cluster_clients,
    setup_log_rotate_for_agents,
)
from tests.multicluster.conftest import cluster_spec_list

MONGODB_PORT = 30000


def test_create_kube_config_file(cluster_clients: Dict, central_cluster_name: str, member_cluster_names: str):
    clients = cluster_clients

    assert len(clients) == 4
    for member_cluster_name in member_cluster_names:
        assert member_cluster_name in clients
    assert central_cluster_name in clients


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_create_mongodb_multi(mongodb_multi: MongoDBMulti | MongoDB):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=2000)


def test_statefulset_is_created_across_multiple_clusters(
    mongodb_multi: MongoDBMulti | MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    def statefulsets_are_ready():
        statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)
        cluster_one_client = member_cluster_clients[0]
        cluster_one_sts = statefulsets[cluster_one_client.cluster_name]

        cluster_two_client = member_cluster_clients[1]
        cluster_two_sts = statefulsets[cluster_two_client.cluster_name]

        cluster_three_client = member_cluster_clients[2]
        cluster_three_sts = statefulsets[cluster_three_client.cluster_name]

        if (
            cluster_one_sts.status.ready_replicas == 2
            and cluster_two_sts.status.ready_replicas == 1
            and cluster_three_sts.status.ready_replicas == 2
        ):
            return True
        else:
            print("statefulsets are not ready yet")
        return False

    wait_until(statefulsets_are_ready, timeout=600)


def test_pvc_not_created(
    mongodb_multi: MongoDBMulti | MongoDB,
    member_cluster_clients: List[MultiClusterClient],
    namespace: str,
):
    with pytest.raises(kubernetes.client.exceptions.ApiException) as e:
        client.CoreV1Api(api_client=member_cluster_clients[0].api_client).read_namespaced_persistent_volume_claim(
            f"data-{mongodb_multi.name}-{0}-{0}", namespace
        )
        assert e.value.reason == "Not Found"


def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti | MongoDB):
    tester = mongodb_multi.tester(port=MONGODB_PORT)
    tester.assert_connectivity()


def test_statefulset_overrides(mongodb_multi: MongoDBMulti | MongoDB, member_cluster_clients: List[MultiClusterClient]):
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)
    # assert sts.podspec override in cluster1
    cluster_one_client = member_cluster_clients[0]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]
    assert_container_in_sts("sidecar1", cluster_one_sts)


def test_headless_service_creation(
    mongodb_multi: MongoDBMulti | MongoDB,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    headless_services = mongodb_multi.read_headless_services(member_cluster_clients)

    cluster_one_client = member_cluster_clients[0]
    cluster_one_svc = headless_services[cluster_one_client.cluster_name]
    ep_one = client.CoreV1Api(api_client=cluster_one_client.api_client).read_namespaced_endpoints(
        cluster_one_svc.metadata.name, namespace
    )
    assert len(ep_one.subsets[0].addresses) == mongodb_multi.get_item_spec(cluster_one_client.cluster_name)["members"]

    cluster_two_client = member_cluster_clients[1]
    cluster_two_svc = headless_services[cluster_two_client.cluster_name]
    ep_two = client.CoreV1Api(api_client=cluster_two_client.api_client).read_namespaced_endpoints(
        cluster_two_svc.metadata.name, namespace
    )
    assert len(ep_two.subsets[0].addresses) == mongodb_multi.get_item_spec(cluster_two_client.cluster_name)["members"]


def test_mongodb_options(mongodb_multi: MongoDBMulti | MongoDB):
    automation_config_tester = mongodb_multi.get_automation_config_tester()
    for process in automation_config_tester.get_replica_set_processes(mongodb_multi.name):
        assert process["args2_6"]["systemLog"]["verbosity"] == 4
        assert process["args2_6"]["systemLog"]["logAppend"]
        assert process["args2_6"]["operationProfiling"]["mode"] == "slowOp"
        assert process["args2_6"]["net"]["port"] == MONGODB_PORT
        assert_log_rotation_process(process)


def test_update_additional_options(
    mongodb_multi: MongoDBMulti | MongoDB, central_cluster_client: kubernetes.client.ApiClient
):
    mongodb_multi["spec"]["additionalMongodConfig"]["systemLog"]["verbosity"] = 2
    mongodb_multi["spec"]["additionalMongodConfig"]["net"]["maxIncomingConnections"] = 100
    # update uses json merge+patch which means that deleting keys is done by setting them to None
    mongodb_multi["spec"]["additionalMongodConfig"]["operationProfiling"] = None

    mongodb_multi.update()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


def test_mongodb_options_were_updated(mongodb_multi: MongoDBMulti | MongoDB):
    automation_config_tester = mongodb_multi.get_automation_config_tester()
    for process in automation_config_tester.get_replica_set_processes(mongodb_multi.name):
        assert process["args2_6"]["systemLog"]["verbosity"] == 2
        assert process["args2_6"]["systemLog"]["logAppend"]
        assert process["args2_6"]["net"]["maxIncomingConnections"] == 100
        assert process["args2_6"]["net"]["port"] == MONGODB_PORT
        # the mode setting has been removed
        assert "mode" not in process["args2_6"]["operationProfiling"]


def test_delete_member_cluster_sts(
    namespace: str,
    mongodb_multi: MongoDBMulti | MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    sts_name = "{}-0".format(mongodb_multi.name)
    api_client = member_cluster_clients[0].api_client
    sts_old = get_statefulset(namespace, sts_name, api_client)
    delete_statefulset(
        namespace=namespace,
        name=sts_name,
        api_client=api_client,
    )

    def check_if_sts_was_recreated() -> bool:
        try:
            sts = get_statefulset(namespace, sts_name, api_client)
            return sts.metadata.uid != sts_old.metadata.uid
        except ApiException as e:
            if e.status == 404:
                return False
            else:
                raise e

    wait_until(check_if_sts_was_recreated, timeout=120)

    # the operator should reconcile and recreate the statefulset
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=400)


def test_cleanup_on_mdbm_delete(
    mongodb_multi: MongoDBMulti | MongoDB, member_cluster_clients: List[MultiClusterClient]
):
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)
    cluster_one_client = member_cluster_clients[0]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]

    mongodb_multi.delete()

    def check_sts_not_exist():
        try:
            client.AppsV1Api(api_client=cluster_one_client.api_client).read_namespaced_stateful_set(
                cluster_one_sts.metadata.name, cluster_one_sts.metadata.namespace
            )
        except ApiException as e:
            if e.reason == "Not Found":
                return True
            return False
        else:
            return False

    KubernetesTester.wait_until(check_sts_not_exist, timeout=200)


def assert_container_in_sts(container_name: str, sts: client.V1StatefulSet):
    container_names = [c.name for c in sts.spec.template.spec.containers]
    assert container_name in container_names
