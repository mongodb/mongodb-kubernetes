import tempfile
import kubernetes

from pytest import mark, fixture
from typing import List
from kubetester.certs import (
    create_multi_cluster_mongodb_tls_certs,
    create_multi_cluster_agent_certs,
    create_multi_cluster_x509_user_cert,
)

from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.kubetester import skip_if_local
from kubetester.mongotester import with_tls
from kubetester.operator import Operator
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongodb_user import MongoDBUser


CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"
AGENT_BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-agent-certs"
CLUSTER_BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-clusterfile"


@fixture(scope="module")
def mongodb_multi_unmarshalled(namespace: str) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace
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
def agent_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
    central_cluster_client: kubernetes.client.ApiClient,
):
    return create_multi_cluster_agent_certs(
        multi_cluster_issuer,
        AGENT_BUNDLE_SECRET_NAME,
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

    return create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        CLUSTER_BUNDLE_SECRET_NAME,
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
def mongodb_x509_user(
    central_cluster_client: kubernetes.client.ApiClient, namespace: str
) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(
        yaml_fixture("mongodb-x509-user.yaml"), "multi-replica-set-x509-user", namespace
    )
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource.create()


@mark.e2e_multi_cluster_tls_with_x509
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_tls_with_x509
def test_deploy_mongodb_multi_with_tls(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_multi_cluster_tls_with_x509
def test_mongodb_multi_tls_enable_x509(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    agent_certs: str,
):
    mongodb_multi.load()

    mongodb_multi["spec"]["security"] = {
        "authentication": {
            "enabled": True,
            "modes": ["X509"],
            "agents": {"mode": "X509"},
        }
    }
    mongodb_multi.update()

    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=50)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_multi_cluster_tls_with_x509
def test_create_mongodb_x509_user(
    central_cluster_client: kubernetes.client.ApiClient,
    mongodb_x509_user: MongoDBUser,
    namespace: str,
):
    mongodb_x509_user.assert_reaches_phase(Phase.Updated, timeout=100)


@skip_if_local
@mark.e2e_multi_cluster_tls_with_x509
def test_x509_user_connectivity(
    mongodb_multi: MongoDBMulti,
    central_cluster_client: kubernetes.client.ApiClient,
    multi_cluster_issuer: str,
    namespace: str,
    ca_path: str,
):
    with tempfile.NamedTemporaryFile(delete=False, mode="w") as cert_file:
        create_multi_cluster_x509_user_cert(
            multi_cluster_issuer, namespace, central_cluster_client, path=cert_file.name
        )
        tester = mongodb_multi.tester()
        tester.assert_x509_authentication(
            cert_file_name=cert_file.name, ssl_ca_certs=ca_path
        )


@mark.e2e_multi_cluster_tls_with_x509
def test_mongodb_multi_tls_enable_internal_cluster_x509(
    mongodb_multi: MongoDBMulti, namespace: str, cluster_certs: str
):
    mongodb_multi.load()

    mongodb_multi["spec"]["security"]["authentication"]["internalCluster"] = "X509"
    mongodb_multi.update()

    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=50)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1000)
