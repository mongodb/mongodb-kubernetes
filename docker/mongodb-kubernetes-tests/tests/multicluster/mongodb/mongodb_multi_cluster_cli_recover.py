from typing import Callable, List

import kubernetes
import pytest
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_cli_recover as testhelper

RESOURCE_NAME = "multi-replica-set"
BUNDLE_SECRET_NAME = f"prefix-{RESOURCE_NAME}-cert"


@pytest.fixture(scope="module")
def mongodb_multi_unmarshalled(
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    central_cluster_client: kubernetes.client.ApiClient,
    member_cluster_names: List[str],
    custom_mdb_version: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace)
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
    mongodb_multi_unmarshalled["spec"]["clusterSpecList"].pop()
    mongodb_multi_unmarshalled.update()
    return mongodb_multi_unmarshalled


@pytest.mark.e2e_mongodb_multi_cluster_recover
def test_deploy_operator(
    install_multi_cluster_operator_set_members_fn: Callable[[List[str]], Operator],
    member_cluster_names: List[str],
    namespace: str,
):
    testhelper.test_deploy_operator(install_multi_cluster_operator_set_members_fn, member_cluster_names, namespace)


@pytest.mark.e2e_mongodb_multi_cluster_recover
def test_create_mongodb_multi(mongodb_multi: MongoDB):
    testhelper.test_create_mongodb_multi(mongodb_multi)


@pytest.mark.e2e_mongodb_multi_cluster_recover
def test_recover_operator_add_cluster(
    member_cluster_names: List[str],
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    testhelper.test_recover_operator_add_cluster(member_cluster_names, namespace, central_cluster_client)


@pytest.mark.e2e_mongodb_multi_cluster_recover
def test_mongodb_multi_recovers_adding_cluster(mongodb_multi: MongoDB, member_cluster_names: List[str]):
    testhelper.test_mongodb_multi_recovers_adding_cluster(mongodb_multi, member_cluster_names)


@pytest.mark.e2e_mongodb_multi_cluster_recover
def test_recover_operator_remove_cluster(
    member_cluster_names: List[str],
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    testhelper.test_recover_operator_remove_cluster(member_cluster_names, namespace, central_cluster_client)


@pytest.mark.e2e_mongodb_multi_cluster_recover
def test_mongodb_multi_recovers_removing_cluster(mongodb_multi: MongoDB, member_cluster_names: List[str]):
    testhelper.test_mongodb_multi_recovers_removing_cluster(mongodb_multi, member_cluster_names)
