from typing import List

import kubernetes
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDB
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_enable_tls as testhelper

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"


@fixture(scope="module")
def mongodb_multi_unmarshalled(namespace: str, member_cluster_names, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
    return resource


@fixture(scope="module")
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


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_multi_unmarshalled: MongoDB,
) -> MongoDB:

    resource = mongodb_multi_unmarshalled
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource.create()


@mark.e2e_mongodb_multi_cluster_enable_tls
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@mark.e2e_mongodb_multi_cluster_enable_tls
def test_create_mongodb_multi(mongodb_multi: MongoDB, namespace: str):
    testhelper.test_create_mongodb_multi(mongodb_multi, namespace)


@mark.e2e_mongodb_multi_cluster_enable_tls
def test_enabled_tls_mongodb_multi(
    mongodb_multi: MongoDB,
    namespace: str,
    server_certs: str,
    multi_cluster_issuer_ca_configmap: str,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_enabled_tls_mongodb_multi(
        mongodb_multi, namespace, server_certs, multi_cluster_issuer_ca_configmap, member_cluster_clients
    )
