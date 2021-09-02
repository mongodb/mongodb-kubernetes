from pytest import mark, fixture
from typing import List
from kubetester import read_secret
from kubetester.certs import create_multi_cluster_mongodb_tls_certs
import kubernetes
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.kubetester import skip_if_local
from kubetester.mongotester import with_tls
from kubetester.operator import Operator
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase

BUNDLE_SECRET_NAME = "prefix-multi-cluster-replica-set-cert"


@fixture(scope="module")
def mongodb_multi_unmarshalled(namespace: str) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"), "multi-cluster-replica-set", namespace
    )
    return resource


@fixture(scope="module")
def server_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):

    return create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        BUNDLE_SECRET_NAME,
        member_cluster_clients,
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
        "tls": {
            "enabled": True,
            "secretRef": {"prefix": "prefix"},
            "ca": multi_cluster_issuer_ca_configmap,
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource.create()


@mark.e2e_multi_cluster_tls
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_tls
def test_deploy_mongodb_multi_with_tls(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):

    # assert for the present of secret object in each member cluster with the certificates
    for client in member_cluster_clients:
        read_secret(
            namespace=namespace, name=BUNDLE_SECRET_NAME, api_client=client.api_client
        )

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=900)


@skip_if_local
@mark.e2e_multi_cluster_tls
def test_tls_connectivity(mongodb_multi: MongoDBMulti, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(opts=[with_tls(use_tls=True, ca_path=ca_path)])
