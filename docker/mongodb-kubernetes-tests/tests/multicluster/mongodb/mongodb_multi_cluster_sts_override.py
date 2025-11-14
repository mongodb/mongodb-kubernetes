from typing import List

import kubernetes
import pytest
from kubernetes import client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator

from ..shared import multi_cluster_sts_override as testhelper

MDB_RESOURCE = "multi-replica-set-sts-override"


@pytest.fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("mongodb-multi-sts-override.yaml"),
        MDB_RESOURCE,
        namespace,
    )
    resource.set_version(custom_mdb_version)

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource.update()


@pytest.mark.e2e_mongodb_multi_sts_override
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@pytest.mark.e2e_mongodb_multi_sts_override
def test_create_mongodb_multi(mongodb_multi: MongoDB):
    testhelper.test_create_mongodb_multi(mongodb_multi)


@pytest.mark.e2e_mongodb_multi_sts_override
def test_statefulset_overrides(mongodb_multi: MongoDB, member_cluster_clients: List[MultiClusterClient]):
    testhelper.test_statefulset_overrides(mongodb_multi, member_cluster_clients)


@pytest.mark.e2e_mongodb_multi_sts_override
def test_access_modes_pvc(
    mongodb_multi: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
    namespace: str,
):
    testhelper.test_access_modes_pvc(mongodb_multi, member_cluster_clients, namespace)


def assert_container_in_sts(container_name: str, sts: client.V1StatefulSet):
    testhelper.assert_container_in_sts(container_name, sts)
