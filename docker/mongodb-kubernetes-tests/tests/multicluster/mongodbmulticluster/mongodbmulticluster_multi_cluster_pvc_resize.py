from typing import List

import kubernetes
import pytest
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_pvc_resize as testhelper

RESOURCE_NAME = "multi-replica-set-pvc-resize"


@pytest.fixture(scope="module")
def mongodb_multi(
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: list[str],
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodbmulticluster-multi-pvc-resize.yaml"), RESOURCE_NAME, namespace
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    try_load(resource)

    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    return resource


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_pvc_resize
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_pvc_resize
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    testhelper.test_create_mongodb_multi(mongodb_multi)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_pvc_resize
def test_mongodb_multi_resize_pvc_state_changes(mongodb_multi: MongoDBMulti):
    testhelper.test_mongodb_multi_resize_pvc_state_changes(mongodb_multi)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_pvc_resize
def test_mongodb_multi_resize_finished(
    mongodb_multi: MongoDBMulti, namespace: str, member_cluster_clients: List[MultiClusterClient]
):
    testhelper.test_mongodb_multi_resize_finished(mongodb_multi, namespace, member_cluster_clients)
