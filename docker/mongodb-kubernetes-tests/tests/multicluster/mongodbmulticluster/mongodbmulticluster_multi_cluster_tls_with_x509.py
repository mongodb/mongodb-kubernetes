from typing import List

import kubernetes
from kubetester.certs_mongodb_multi import (
    create_multi_cluster_mongodb_x509_tls_certs,
    create_multi_cluster_x509_agent_certs,
)
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_tls_with_x509 as testhelper

# TODO This test needs to re-introduce certificate rotation and enabling authentication step by step
# See https://jira.mongodb.org/browse/CLOUDP-311366

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"
AGENT_BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-agent-certs"
CLUSTER_BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-clusterfile"


@fixture(scope="module")
def mongodb_multi_unmarshalled(namespace: str, member_cluster_names, custom_mdb_version: str) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(ensure_ent_version(custom_mdb_version))

    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
    resource["spec"]["additionalMongodConfig"] = {"net": {"tls": {"mode": "requireTLS"}}}

    return resource


@fixture(scope="module")
def server_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):

    return create_multi_cluster_mongodb_x509_tls_certs(
        multi_cluster_issuer,
        BUNDLE_SECRET_NAME,
        member_cluster_clients,
        central_cluster_client,
        mongodb_multi_unmarshalled,
    )


@fixture(scope="module")
def cluster_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):

    return create_multi_cluster_mongodb_x509_tls_certs(
        multi_cluster_issuer,
        CLUSTER_BUNDLE_SECRET_NAME,
        member_cluster_clients,
        central_cluster_client,
        mongodb_multi_unmarshalled,
    )


@fixture(scope="module")
def agent_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):
    return create_multi_cluster_x509_agent_certs(
        multi_cluster_issuer,
        AGENT_BUNDLE_SECRET_NAME,
        central_cluster_client,
        mongodb_multi_unmarshalled,
    )


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    server_certs: str,
    agent_certs: str,
    cluster_certs: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    multi_cluster_issuer_ca_configmap: str,
    member_cluster_names,
) -> MongoDBMulti:

    resource = mongodb_multi_unmarshalled
    resource["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "tls": {
            "ca": multi_cluster_issuer_ca_configmap,
        },
        "authentication": {
            "enabled": True,
            "modes": ["X509", "SCRAM"],
            "agents": {"mode": "X509"},
            "internalCluster": "X509",
        },
    }

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.update()

    return resource


@fixture(scope="module")
def mongodb_x509_user(central_cluster_client: kubernetes.client.ApiClient, namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodbmulticluster-x509-user.yaml"), "multi-replica-set-x509-user", namespace
    )
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    resource.update()

    return resource


@mark.e2e_mongodbmulticluster_multi_cluster_tls_with_x509
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@mark.e2e_mongodbmulticluster_multi_cluster_tls_with_x509
def test_deploy_mongodb_multi_with_tls_and_authentication(mongodb_multi: MongoDBMulti, namespace: str):
    testhelper.test_deploy_mongodb_multi_with_tls_and_authentication(mongodb_multi, namespace)


@mark.e2e_mongodbmulticluster_multi_cluster_tls_with_x509
def test_ops_manager_state_was_updated_correctly(mongodb_multi: MongoDBMulti):
    testhelper.test_ops_manager_state_was_updated_correctly(mongodb_multi)


@mark.e2e_mongodbmulticluster_multi_cluster_tls_with_x509
def test_create_mongodb_x509_user(
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_x509_user: MongoDBUser,
    namespace: str,
):
    testhelper.test_create_mongodb_x509_user(central_cluster_client, mongodb_x509_user, namespace)


@skip_if_local
@mark.e2e_mongodbmulticluster_multi_cluster_tls_with_x509
def test_x509_user_connectivity(
    mongodb_multi: MongoDBMulti,
    central_cluster_client: kubernetes.client.ApiClient,
    multi_cluster_issuer: str,
    namespace: str,
    ca_path: str,
):
    testhelper.test_x509_user_connectivity(
        mongodb_multi, central_cluster_client, multi_cluster_issuer, namespace, ca_path
    )


# TODO Replace and use this method to check that certificate rotation after enabling TLS and authentication mechanisms
# keeps the resources reachable and in Running state.
def assert_certificate_rotation(central_cluster_client, mongodb_multi, namespace, certificate_name):
    testhelper.assert_certificate_rotation(central_cluster_client, mongodb_multi, namespace, certificate_name)
