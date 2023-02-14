from typing import Dict, List
import kubernetes
import pytest
from kubernetes import client
from kubernetes.client.rest import ApiException

from kubetester import create_or_update
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from kubetester.kubetester import (
    fixture as yaml_fixture,
    skip_if_local,
    KubernetesTester,
)
from kubernetes import client

from tests.multicluster.conftest import cluster_spec_list


@pytest.fixture(scope="module")
def mongodb_multi(
        central_cluster_client: kubernetes.client.ApiClient, namespace: str, member_cluster_names
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi-central-sts-override.yaml"),
        "multi-replica-set",
        namespace,
    )
    resource["spec"]["persistent"] = False
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    # TODO: incorporate this into the base class.
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    create_or_update(resource)
    return resource




@pytest.mark.e2e_multi_cluster_replica_set
def test_create_kube_config_file(
    cluster_clients: Dict, central_cluster_name: str, member_cluster_names: str
):
    clients = cluster_clients

    assert len(clients) == 4
    for member_cluster_name in member_cluster_names:
        assert member_cluster_name in clients
    assert central_cluster_name in clients


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
        client.CoreV1Api(
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


@pytest.mark.e2e_multi_cluster_replica_set
def test_cleanup_on_mdbm_delete(
    mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]
):
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)
    cluster_one_client = member_cluster_clients[0]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]

    mongodb_multi.delete()

    def check_sts_not_exist():
        try:
            client.AppsV1Api(
                api_client=cluster_one_client.api_client
            ).read_namespaced_stateful_set(
                cluster_one_sts.metadata.name, cluster_one_sts.metadata.namespace
            )
        except ApiException as e:
            if e.reason == "Not Found":
                return True
            return False
        else:
            return False

    KubernetesTester.wait_until(check_sts_not_exist, timeout=100)


def assert_container_in_sts(container_name: str, sts: client.V1StatefulSet):
    container_names = [c.name for c in sts.spec.template.spec.containers]
    assert container_name in container_names
