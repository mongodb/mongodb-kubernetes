import tempfile
from typing import List

import kubernetes
from kubetester import create_or_update
from kubetester.automation_config_tester import AutomationConfigTester
from kubetester.certs import (
    Certificate,
    create_multi_cluster_mongodb_x509_tls_certs,
    create_multi_cluster_x509_agent_certs,
    create_multi_cluster_x509_user_cert,
)
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.mongodb_user import MongoDBUser
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.multicluster.conftest import cluster_spec_list

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"
AGENT_BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-agent-certs"
CLUSTER_BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-clusterfile"


@fixture(scope="module")
def mongodb_multi_unmarshalled(namespace: str, member_cluster_names, custom_mdb_version: str) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(ensure_ent_version(custom_mdb_version))

    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1, 2])
    resource["spec"]["additionalMongodConfig"] = {"net": {"tls": {"mode": "preferTLS"}}}

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
    }

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    create_or_update(resource)

    return resource


@fixture(scope="module")
def mongodb_x509_user(central_cluster_client: kubernetes.client.ApiClient, namespace: str) -> MongoDBUser:
    resource = MongoDBUser.from_yaml(yaml_fixture("mongodb-x509-user.yaml"), "multi-replica-set-x509-user", namespace)
    resource["spec"]["mongodbResourceRef"]["name"] = MDB_RESOURCE
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    create_or_update(resource)

    return resource


@mark.e2e_multi_cluster_tls_with_x509
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_tls_with_x509
def test_deploy_mongodb_multi_with_tls(mongodb_multi: MongoDBMulti, namespace: str):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@mark.e2e_multi_cluster_tls_with_x509
def test_rotate_cert_and_assert_mdb_running(
    namespace: str,
    mongodb_multi: MongoDBMulti,
    central_cluster_client: kubernetes.client.ApiClient,
):
    assert_certificate_rotation(central_cluster_client, mongodb_multi, namespace, BUNDLE_SECRET_NAME)


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
            "modes": ["X509", "SCRAM"],
            "agents": {"mode": "X509"},
        }
    }
    create_or_update(mongodb_multi)

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1500)


@mark.e2e_multi_cluster_tls_with_x509
def test_mongodb_multi_tls_enable_internal_cluster_x509(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    cluster_certs: str,
    agent_certs: str,
):
    mongodb_multi.load()

    mongodb_multi["spec"]["security"]["authentication"]["internalCluster"] = "X509"
    create_or_update(mongodb_multi)

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=2100)


@mark.e2e_multi_cluster_tls_with_x509
def test_ops_manager_state_was_updated_correctly(mongodb_multi: MongoDBMulti):
    ac_tester = AutomationConfigTester(KubernetesTester.get_automation_config())
    ac_tester.assert_authentication_enabled(expected_num_deployment_auth_mechanisms=2)
    ac_tester.assert_authentication_mechanism_enabled("MONGODB-X509")
    ac_tester.assert_internal_cluster_authentication_enabled()


@mark.e2e_multi_cluster_tls_with_x509
def test_rotate_certfile_and_assert_mdb_running(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    assert_certificate_rotation(central_cluster_client, mongodb_multi, namespace, CLUSTER_BUNDLE_SECRET_NAME)


def assert_certificate_rotation(central_cluster_client, mongodb_multi, namespace, certificate_name):
    cert = Certificate(name=certificate_name, namespace=namespace)
    cert.api = kubernetes.client.CustomObjectsApi(api_client=central_cluster_client)
    cert.load()
    cert["spec"]["dnsNames"].append("foo")  # Append DNS to cert to rotate the certificate
    cert.update()
    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=100)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1500)


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
        tester.assert_x509_authentication(cert_file_name=cert_file.name, tlsCAFile=ca_path)
