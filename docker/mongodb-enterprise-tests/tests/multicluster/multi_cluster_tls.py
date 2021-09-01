from pytest import mark, fixture
from typing import List
from kubetester import read_secret
from kubetester.certs import create_multi_cluster_mongodb_tls_certs
import kubernetes
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase

BUNDLE_SECRET_NAME = "certs-multi-cluster-replica-set-cert"


@fixture(scope="module")
def mongodb_multi_unmarshalled(namespace: str) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"), "multi-replica-set", namespace
    )
    return resource


@fixture(scope="module")
def server_certs(
    multi_cluster_issuer: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    create_multi_cluster_mongodb_tls_certs(
        multi_cluster_issuer,
        BUNDLE_SECRET_NAME,
        member_cluster_clients,
        mongodb_multi_unmarshalled,
    )
    return "certs"


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    server_certs: str,
    mongodb_multi_unmarshalled: MongoDBMulti,
) -> MongoDBMulti:

    resource = mongodb_multi_unmarshalled
    resource["spec"]["security"] = {
        "tls": {
            "enabled": True,
            "secretRef": {"prefix": server_certs},
        },
    }
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    return resource.create()


@mark.e2e_multi_cluster_tls
def test_deploy_cert_manager_member_clusters(multi_cluster_issuer: str):
    multi_cluster_issuer


@mark.e2e_multi_cluster_tls
def test_deploy_mongodb_multi_with_tls(
    mongodb_multi: MongoDBMulti,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    # TODO: Assert the phase of the CR after integration
    mongodb_multi

    # assert for the present of secret object in each member cluster with the certificates
    for client in member_cluster_clients:
        read_secret(
            namespace=namespace, name=BUNDLE_SECRET_NAME, api_client=client.api_client
        )
