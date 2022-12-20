from typing import Dict, List

import kubernetes
import pytest

from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from kubetester.kubetester import fixture as yaml_fixture, skip_if_local
from kubernetes import client


@pytest.fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient, namespace: str
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"), "multi-replica-set", namespace
    )
    resource["spec"]["persistent"] = False

    # TODO: incorporate this into the base class.
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return resource.create()


@pytest.mark.e2e_multi_cluster_replica_set
def test_create_kube_config_file(cluster_clients: Dict):
    clients = cluster_clients

    assert len(clients) == 4
    assert "e2e.cluster1.mongokubernetes.com" in clients
    assert "e2e.cluster2.mongokubernetes.com" in clients
    assert "e2e.cluster3.mongokubernetes.com" in clients
    assert "e2e.operator.mongokubernetes.com" in clients


@pytest.mark.e2e_multi_cluster_replica_set
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_replica_set
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)


@pytest.mark.e2e_multi_cluster_replica_set
def test_statefulset_is_created_across_multiple_clusters(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)
    cluster_one_client = member_cluster_clients[0]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]
    assert cluster_one_sts.status.ready_replicas == 2

    cluster_two_client = member_cluster_clients[1]
    cluster_two_sts = statefulsets[cluster_two_client.cluster_name]
    assert cluster_two_sts.status.ready_replicas == 1

    cluster_three_client = member_cluster_clients[2]
    cluster_three_sts = statefulsets[cluster_three_client.cluster_name]
    assert cluster_three_sts.status.ready_replicas == 2


@pytest.mark.e2e_multi_cluster_replica_set
def test_pvc_not_created(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    namespace: str,
):
    with pytest.raises(kubernetes.client.exceptions.ApiException) as e:
        pvc = client.CoreV1Api(
            api_client=member_cluster_clients[0].api_client
        ).read_namespaced_persistent_volume_claim(
            f"data-{mongodb_multi.name}-{0}-{0}", namespace
        )
        assert e.value.reason == "Not Found"


@skip_if_local
@pytest.mark.e2e_multi_cluster_replica_set
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


@pytest.mark.e2e_multi_cluster_replica_set
def test_statefulset_overrides(
    mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]
):
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)
    # assert sts.podspec override in cluster1
    cluster_one_client = member_cluster_clients[0]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]
    assert_container_in_sts("sidecar1", cluster_one_sts)


def assert_container_in_sts(container_name: str, sts: client.V1StatefulSet):
    container_names = [c.name for c in sts.spec.template.spec.containers]
    assert container_name in container_names
