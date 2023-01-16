from typing import Dict, List
from pytest import mark, fixture

import kubernetes
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from kubetester.kubetester import KubernetesTester, fixture as yaml_fixture
from kubernetes import client
from kubeobject import CustomObject
import time

from kubetester import delete_pod, get_pod_when_ready
from kubetester.automation_config_tester import AutomationConfigTester


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient, namespace: str
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"), "multi-replica-set", namespace
    )
    resource["spec"]["persistent"] = False
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return resource


@mark.e2e_multi_cluster_disaster_recovery
def test_label_namespace(
    namespace: str, central_cluster_client: kubernetes.client.ApiClient
):
    api = client.CoreV1Api(api_client=central_cluster_client)

    labels = {"istio-injection": "enabled"}
    ns = api.read_namespace(name=namespace)

    ns.metadata.labels.update(labels)
    api.replace_namespace(name=namespace, body=ns)


@mark.e2e_multi_cluster_disaster_recovery
def test_create_service_entry(service_entry: CustomObject):
    service_entry.create()


@mark.e2e_multi_cluster_disaster_recovery
@mark.e2e_multi_cluster_multi_disaster_recovery
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_disaster_recovery
@mark.e2e_multi_cluster_multi_disaster_recovery
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.create()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_multi_cluster_multi_disaster_recovery
def test_block_cluster2_traffic(
    multi_cluster_operator: Operator,
    central_cluster_client: kubernetes.client.ApiClient,
):

    deployment = multi_cluster_operator.read_deployment()
    # add host alias for cluster2 and cluster3 to a non existent IP
    deployment.spec.template.spec.host_aliases = [
        client.V1HostAlias(
            hostnames=[
                "api.e2e.cluster2.mongokubernetes.com",
            ],
            ip="1.2.3.4",
        )
    ]
    client.AppsV1Api(api_client=central_cluster_client).patch_namespaced_deployment(
        multi_cluster_operator.name,
        multi_cluster_operator.namespace,
        deployment,
    )

    multi_cluster_operator._wait_for_operator_ready()


@mark.e2e_multi_cluster_disaster_recovery
def test_update_service_entry_block_cluster3_traffic(service_entry: CustomObject):
    service_entry.load()
    service_entry["spec"]["hosts"] = [
        "cloud-qa.mongodb.com",
        "api.e2e.cluster1.mongokubernetes.com",
        "api.e2e.cluster2.mongokubernetes.com",
    ]
    service_entry.update()


@mark.e2e_multi_cluster_disaster_recovery
def test_mongodb_multi_leaves_running_state(
    mongodb_multi: MongoDBMulti,
):
    mongodb_multi.load()
    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=100)


@mark.e2e_multi_cluster_disaster_recovery
@mark.e2e_multi_cluster_multi_disaster_recovery
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


@mark.e2e_multi_cluster_disaster_recovery
def test_replica_reaches_running(mongodb_multi: MongoDBMulti):
    mongodb_multi.load()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


@mark.e2e_multi_cluster_disaster_recovery
@mark.e2e_multi_cluster_multi_disaster_recovery
def test_number_numbers_in_ac(mongodb_multi: MongoDBMulti):
    tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    desiredmembers = 0
    for c in mongodb_multi["spec"]["clusterSpecList"]:
        desiredmembers += c["members"]

    processes = tester.get_replica_set_processes(mongodb_multi.name)
    assert len(processes) == desiredmembers


@mark.e2e_multi_cluster_disaster_recovery
def test_sts_count_in_member_cluster(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    # assert the distribution of member cluster3 nodes.
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)
    cluster_one_client = member_cluster_clients[0]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]
    assert cluster_one_sts.status.ready_replicas == 3

    cluster_two_client = member_cluster_clients[1]
    cluster_two_sts = statefulsets[cluster_two_client.cluster_name]
    assert cluster_two_sts.status.ready_replicas == 2


@mark.e2e_multi_cluster_multi_disaster_recovery
def test_block_cluster2_and_cluster3_traffic(
    multi_cluster_operator: Operator,
    central_cluster_client: kubernetes.client.ApiClient,
):

    deployment = multi_cluster_operator.read_deployment()
    # add host alias for cluster2 and cluster3 to a non existent IP
    deployment.spec.template.spec.host_aliases = [
        client.V1HostAlias(
            hostnames=[
                "api.e2e.cluster2.mongokubernetes.com",
                "api.e2e.cluster3.mongokubernetes.com",
            ],
            ip="1.2.3.4",
        )
    ]
    client.AppsV1Api(api_client=central_cluster_client).patch_namespaced_deployment(
        multi_cluster_operator.name,
        multi_cluster_operator.namespace,
        deployment,
    )

    multi_cluster_operator._wait_for_operator_ready()


@mark.e2e_multi_cluster_multi_disaster_recovery
def test_replica_abandons_running(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=500)


@mark.e2e_multi_cluster_multi_disaster_recovery
def test_replica_set_reaches_running(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)
