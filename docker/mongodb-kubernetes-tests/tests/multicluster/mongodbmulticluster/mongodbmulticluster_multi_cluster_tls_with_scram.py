from typing import List

import kubernetes
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_tls_with_scram as testhelper

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"
USER_NAME = "my-user-1"
USER_RESOURCE = "multi-replica-set-scram-user"
PASSWORD_SECRET_NAME = "mms-user-1-password"


@fixture(scope="module")
def mongodb_multi_unmarshalled(
    namespace: str,
    member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["clusterSpecList"] = cluster_spec_list(
        member_cluster_names=member_cluster_names, members=[2, 1, 2]
    )

    return resource


@fixture(scope="module")
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


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    server_certs: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    multi_cluster_issuer_ca_configmap: str,
) -> MongoDBMulti:

    resource = mongodb_multi_unmarshalled
    resource["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "tls": {
            "ca": multi_cluster_issuer_ca_configmap,
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource.create()


@fixture(scope="module")
def mongodb_user(central_cluster_client: kubernetes.client.ApiClient, namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(yaml_fixture("mongodb-user.yaml"), USER_RESOURCE, namespace)

    resource["spec"]["username"] = USER_NAME
    resource["spec"]["passwordSecretKeyRef"] = {
        "name": PASSWORD_SECRET_NAME,
        "key": "password",
    }
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE
    resource["spec"]["mongodbResourceRef"]["namespace"] = namespace
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource.create()


@mark.e2e_mongodbmulticluster_multi_cluster_tls_with_scram
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@mark.e2e_mongodbmulticluster_multi_cluster_tls_with_scram
def test_deploy_mongodb_multi_with_tls(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):

    testhelper.test_deploy_mongodb_multi_with_tls(mongodb_multi, namespace, member_cluster_clients)


@mark.e2e_mongodbmulticluster_multi_cluster_tls_with_scram
def test_update_mongodb_multi_tls_with_scram(
    mongodb_multi: MongoDBMulti,
    namespace: str,
):

    testhelper.test_update_mongodb_multi_tls_with_scram(mongodb_multi, namespace)


@mark.e2e_mongodbmulticluster_multi_cluster_tls_with_scram
def test_create_mongodb_user(
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_user: MongoDBUser,
    namespace: str,
):
    testhelper.test_create_mongodb_user(central_cluster_client, mongodb_user, namespace)


@skip_if_local
@mark.e2e_mongodbmulticluster_multi_cluster_tls_with_scram
def test_tls_connectivity(mongodb_multi: MongoDBMulti, ca_path: str):
    testhelper.test_create_mongodb_user(mongodb_multi, ca_path)


@skip_if_local
@mark.e2e_mongodbmulticluster_multi_cluster_tls_with_scram
def test_replica_set_connectivity_with_scram_and_tls(mongodb_multi: MongoDBMulti, ca_path: str):
    testhelper.test_replica_set_connectivity_with_scram_and_tls(mongodb_multi, ca_path)


@skip_if_local
@mark.e2e_mongodbmulticluster_multi_cluster_tls_with_scram
def test_replica_set_connectivity_from_connection_string_standard(
    namespace: str,
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    ca_path: str,
):
    testhelper.test_replica_set_connectivity_from_connection_string_standard(
        namespace, mongodb_multi, member_cluster_clients, ca_path
    )


@skip_if_local
@mark.e2e_mongodbmulticluster_multi_cluster_tls_with_scram
def test_replica_set_connectivity_from_connection_string_standard_srv(
    namespace: str,
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    ca_path: str,
):
    testhelper.test_replica_set_connectivity_from_connection_string_standard_srv(
        namespace, mongodb_multi, member_cluster_clients, ca_path
    )


@mark.e2e_mongodbmulticluster_multi_cluster_tls_with_scram
def test_mongodb_multi_tls_enable_x509(
    mongodb_multi: MongoDBMulti,
    namespace: str,
):
    testhelper.test_mongodb_multi_tls_enable_x509(mongodb_multi, namespace)


@mark.e2e_mongodbmulticluster_multi_cluster_tls_with_scram
def test_mongodb_multi_tls_automation_config_was_updated(
    mongodb_multi: MongoDBMulti,
    namespace: str,
):
    testhelper.test_mongodb_multi_tls_automation_config_was_updated(mongodb_multi, namespace)
