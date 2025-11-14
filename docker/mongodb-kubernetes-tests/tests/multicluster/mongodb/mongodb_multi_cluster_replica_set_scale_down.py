from typing import List

import kubernetes
import pytest
from kubetester import try_load
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_replica_set_scale_down as testhelper

RESOURCE_NAME = "multi-replica-set"
BUNDLE_SECRET_NAME = f"prefix-{RESOURCE_NAME}-cert"


@pytest.fixture(scope="module")
def mongodb_multi_unmarshalled(
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace)
    resource.set_version(custom_mdb_version)
    # start at one member in each cluster
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])

    resource["spec"]["security"] = {
        "certsSecretPrefix": "prefix",
        "tls": {
            "ca": multi_cluster_issuer_ca_configmap,
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource


@pytest.fixture(scope="module")
def server_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):

    return create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        BUNDLE_SECRET_NAME,
        member_cluster_clients,
        central_cluster_client,
        mongodb_multi_unmarshalled,
    )


@pytest.fixture(scope="module")
def mongodb_multi(mongodb_multi_unmarshalled: MongoDB, server_certs: str) -> MongoDB:
    if try_load(mongodb_multi_unmarshalled):
        return mongodb_multi_unmarshalled

    return mongodb_multi_unmarshalled.update()


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_scale_down
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_scale_down
def test_create_mongodb_multi(mongodb_multi: MongoDB):
    testhelper.test_create_mongodb_multi(mongodb_multi)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_scale_down
def test_statefulsets_have_been_created_correctly(
    mongodb_multi: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_statefulsets_have_been_created_correctly(mongodb_multi, member_cluster_clients)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_scale_down
def test_ops_manager_has_been_updated_correctly_before_scaling():
    testhelper.test_ops_manager_has_been_updated_correctly_before_scaling()


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_scale_down
def test_scale_mongodb_multi(mongodb_multi: MongoDB):
    testhelper.test_scale_mongodb_multi(mongodb_multi)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_scale_down
def test_statefulsets_have_been_scaled_down_correctly(
    mongodb_multi: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_statefulsets_have_been_scaled_down_correctly(mongodb_multi, member_cluster_clients)


@pytest.mark.e2e_mongodb_multi_cluster_replica_set_scale_down
def test_ops_manager_has_been_updated_correctly_after_scaling():
    testhelper.test_ops_manager_has_been_updated_correctly_after_scaling()


@skip_if_local
@pytest.mark.e2e_mongodb_multi_cluster_replica_set_scale_down
def test_replica_set_is_reachable(mongodb_multi: MongoDB, ca_path: str):
    testhelper.test_replica_set_is_reachable(mongodb_multi, ca_path)
