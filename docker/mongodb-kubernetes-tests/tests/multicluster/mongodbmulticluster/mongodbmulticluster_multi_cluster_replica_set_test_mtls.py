from typing import List

import kubernetes
import pytest
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_replica_set_test_mtls as testhelper

MDB_RESOURCE = "multi-replica-set"


@pytest.fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(custom_mdb_version)

    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    # TODO: incorporate this into the base class.
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.update()
    return resource


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_mtls_test
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_mtls_test
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    testhelper.test_create_mongodb_multi(mongodb_multi)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_mtls_test
def test_create_mongo_pod_in_separate_namespace(
    member_cluster_clients: List[MultiClusterClient],
    evergreen_task_id: str,
    namespace: str,
):
    testhelper.test_create_mongo_pod_in_separate_namespace(member_cluster_clients, evergreen_task_id, namespace)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_mtls_test
def test_connectivity_fails_from_second_namespace(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    namespace: str,
):
    testhelper.test_connectivity_fails_from_second_namespace(mongodb_multi, member_cluster_clients, namespace)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_mtls_test
def test_enable_istio_injection(
    member_cluster_clients: List[MultiClusterClient],
    namespace: str,
):
    testhelper.test_enable_istio_injection(member_cluster_clients, namespace)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_mtls_test
def test_delete_existing_mongo_pod(member_cluster_clients: List[MultiClusterClient], namespace: str):
    testhelper.test_delete_existing_mongo_pod(member_cluster_clients, namespace)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_mtls_test
def test_create_pod_with_istio_sidecar(member_cluster_clients: List[MultiClusterClient], namespace: str):
    testhelper.test_create_pod_with_istio_sidecar(member_cluster_clients, namespace)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_mtls_test
def test_connectivity_succeeds_from_second_namespace(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    namespace: str,
):
    testhelper.test_connectivity_succeeds_from_second_namespace(mongodb_multi, member_cluster_clients, namespace)
