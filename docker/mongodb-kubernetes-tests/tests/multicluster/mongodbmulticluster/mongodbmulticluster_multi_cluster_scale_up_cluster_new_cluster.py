from typing import Callable, List

import kubernetes
import pytest
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_scale_up_cluster_new_cluster as testhelper

RESOURCE_NAME = "multi-replica-set"
BUNDLE_SECRET_NAME = f"prefix-{RESOURCE_NAME}-cert"


@pytest.fixture(scope="module")
def mongodb_multi_unmarshalled(
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi.yaml"), RESOURCE_NAME, namespace)
    resource.set_version(custom_mdb_version)
    # ensure certs are created for the members during scale up
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
    mongodb_multi_unmarshalled: MongoDBMulti,
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
def mongodb_multi(mongodb_multi_unmarshalled: MongoDBMulti, server_certs: str) -> MongoDBMulti:
    mongodb_multi_unmarshalled["spec"]["clusterSpecList"].pop()
    return mongodb_multi_unmarshalled.create()


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_scale_up_cluster_new_cluster
def test_deploy_operator(
    install_multi_cluster_operator_set_members_fn: Callable[[List[str]], Operator],
    member_cluster_names: List[str],
    namespace: str,
):
    testhelper.test_deploy_operator(install_multi_cluster_operator_set_members_fn, member_cluster_names, namespace)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_scale_up_cluster_new_cluster
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    testhelper.test_create_mongodb_multi(mongodb_multi)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_scale_up_cluster_new_cluster
def test_statefulsets_have_been_created_correctly(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_statefulsets_have_been_created_correctly(mongodb_multi, member_cluster_clients)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_scale_up_cluster_new_cluster
def test_ops_manager_has_been_updated_correctly_before_scaling():
    testhelper.test_ops_manager_has_been_updated_correctly_before_scaling()


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_scale_up_cluster_new_cluster
def test_delete_deployment(namespace: str, central_cluster_client: kubernetes.client.ApiClient):
    testhelper.test_delete_deployment(namespace, central_cluster_client)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_scale_up_cluster_new_cluster
def test_re_deploy_operator(
    install_multi_cluster_operator_set_members_fn: Callable[[List[str]], Operator],
    member_cluster_names: List[str],
    namespace: str,
):
    testhelper.test_re_deploy_operator(install_multi_cluster_operator_set_members_fn, member_cluster_names, namespace)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_scale_up_cluster_new_cluster
def test_add_new_cluster_to_mongodb_multi_resource(
    mongodb_multi: MongoDBMulti, member_cluster_clients: List[MultiClusterClient]
):
    testhelper.test_re_deploy_operator(mongodb_multi, member_cluster_clients)


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_scale_up_cluster_new_cluster
def test_statefulsets_have_been_created_correctly_after_cluster_addition(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_statefulsets_have_been_created_correctly_after_cluster_addition(
        mongodb_multi, member_cluster_clients
    )


@pytest.mark.e2e_mongodbmulticluster_multi_cluster_scale_up_cluster_new_cluster
def test_ops_manager_has_been_updated_correctly_after_scaling():
    testhelper.test_ops_manager_has_been_updated_correctly_after_scaling()


@skip_if_local
@pytest.mark.e2e_mongodbmulticluster_multi_cluster_scale_up_cluster_new_cluster
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti, ca_path: str):
    testhelper.test_replica_set_is_reachable(mongodb_multi, ca_path)
