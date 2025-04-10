from typing import List

import kubernetes
import pytest
from kubernetes import client
from kubetester import get_statefulset, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from tests.multicluster.conftest import cluster_spec_list

RESOURCE_NAME = "multi-replica-set-pvc-resize"
RESIZED_STORAGE_SIZE = "2Gi"


@pytest.fixture(scope="module")
def mongodb_multi(
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: list[str],
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi-pvc-resize.yaml"), RESOURCE_NAME, namespace)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    try_load(resource)

    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    return resource


@pytest.mark.e2e_multi_cluster_pvc_resize
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_pvc_resize
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=2000)


@pytest.mark.e2e_multi_cluster_pvc_resize
def test_mongodb_multi_resize_pvc_state_changes(mongodb_multi: MongoDBMulti):
    # Update the resource
    mongodb_multi.load()
    mongodb_multi["spec"]["statefulSet"]["spec"]["volumeClaimTemplates"][0]["spec"]["resources"]["requests"][
        "storage"
    ] = RESIZED_STORAGE_SIZE
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Pending, timeout=400)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@pytest.mark.e2e_multi_cluster_pvc_resize
def test_mongodb_multi_resize_finished(
    mongodb_multi: MongoDBMulti, namespace: str, member_cluster_clients: List[MultiClusterClient]
):
    statefulsets = []
    for i, c in enumerate(member_cluster_clients):
        statefulsets.append((get_statefulset(namespace, f"{RESOURCE_NAME}-{i}", c.api_client), c.api_client))

    for sts, c in statefulsets:
        assert sts.spec.volume_claim_templates[0].spec.resources.requests["storage"] == RESIZED_STORAGE_SIZE
        first_pvc_name = f"data-{sts.metadata.name}-0"
        pvc = client.CoreV1Api(api_client=c).read_namespaced_persistent_volume_claim(first_pvc_name, namespace)
        assert pvc.status.capacity["storage"] == RESIZED_STORAGE_SIZE
