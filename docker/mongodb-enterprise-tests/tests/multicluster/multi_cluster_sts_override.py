from typing import List

import kubernetes
import pytest
from kubernetes import client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator


@pytest.fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi-sts-override.yaml"),
        "multi-replica-set-sts-override",
        namespace,
    )
    resource.set_version(custom_mdb_version)

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource.update()


@pytest.mark.e2e_multi_sts_override
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_sts_override
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@pytest.mark.e2e_multi_sts_override
def test_statefulset_overrides(mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]):
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)

    # assert sts.podspec override in cluster1
    cluster_one_client = member_cluster_clients[0]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]
    assert_container_in_sts("sidecar1", cluster_one_sts)
    assert "multi-replica-set" in cluster_one_sts.spec.template.metadata.labels["app"]

    # assert sts.podspec override in cluster2
    cluster_two_client = member_cluster_clients[1]
    cluster_two_sts = statefulsets[cluster_two_client.cluster_name]
    assert_container_in_sts("sidecar2", cluster_two_sts)


@pytest.mark.e2e_multi_sts_override
def test_access_modes_pvc(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    namespace: str,
):
    pvc = client.CoreV1Api(api_client=member_cluster_clients[0].api_client).read_namespaced_persistent_volume_claim(
        f"data-{mongodb_multi.name}-{0}-{0}", namespace
    )

    assert "ReadWriteOnce" in pvc.spec.access_modes


def assert_container_in_sts(container_name: str, sts: client.V1StatefulSet):
    container_names = [c.name for c in sts.spec.template.spec.containers]
    assert container_name in container_names
