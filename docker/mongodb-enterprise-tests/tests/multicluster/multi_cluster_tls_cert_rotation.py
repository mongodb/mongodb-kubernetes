from pytest import mark, fixture
from typing import List
from kubetester.certs import create_multi_cluster_mongodb_tls_certs, Certificate
import kubernetes
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.kubetester import skip_if_local
from kubetester.mongotester import with_tls
from kubetester.operator import Operator
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"


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


@mark.e2e_multi_cluster_tls_cert_rotation
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_tls_cert_rotation
def test_deploy_mongodb_multi_with_tls(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=900)


@skip_if_local
@mark.e2e_multi_cluster_tls_cert_rotation
def test_tls_connectivity(mongodb_multi: MongoDBMulti, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(opts=[with_tls(use_tls=True, ca_path=ca_path)])


@mark.e2e_multi_cluster_tls_cert_rotation
def test_rotate_cert_and_assert_mdb_running(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    cert = Certificate(name=BUNDLE_SECRET_NAME, namespace=namespace)
    cert.api = kubernetes.client.CustomObjectsApi(api_client=central_cluster_client)
    cert.load()
    cert["spec"]["dnsNames"].append(
        "foo"
    )  # Append DNS to cert to rotate the certificate
    cert.update()

    mongodb_multi.assert_abandons_phase(Phase.Running, timeout=60)
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=900)
