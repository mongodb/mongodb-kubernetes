from typing import List

import kubernetes
from kubernetes import client
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDB
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_cluster_tls_no_mesh as testhelper

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"


@fixture(scope="module")
def mongodb_multi_unmarshalled(namespace: str, member_cluster_names: List[str], custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    # These domains map 1:1 to the CoreDNS file. Please be mindful when updating them.
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 2, 2])

    resource["spec"]["externalAccess"] = {}
    resource["spec"]["clusterSpecList"][0]["externalAccess"] = {
        "externalDomain": "kind-e2e-cluster-1.interconnected",
        "externalService": {
            "spec": {
                "type": "LoadBalancer",
                "publishNotReadyAddresses": False,
                "ports": [
                    {
                        "name": "mongodb",
                        "port": 27017,
                    },
                    {
                        "name": "backup",
                        "port": 27018,
                    },
                    {
                        "name": "testing0",
                        "port": 27019,
                    },
                ],
            }
        },
    }
    resource["spec"]["clusterSpecList"][1]["externalAccess"] = {
        "externalDomain": "kind-e2e-cluster-2.interconnected",
        "externalService": {
            "spec": {
                "type": "LoadBalancer",
                "publishNotReadyAddresses": False,
                "ports": [
                    {
                        "name": "mongodb",
                        "port": 27017,
                    },
                    {
                        "name": "backup",
                        "port": 27018,
                    },
                    {
                        "name": "testing1",
                        "port": 27019,
                    },
                ],
            }
        },
    }
    resource["spec"]["clusterSpecList"][2]["externalAccess"] = {
        "externalDomain": "kind-e2e-cluster-3.interconnected",
        "externalService": {
            "spec": {
                "type": "LoadBalancer",
                "publishNotReadyAddresses": False,
                "ports": [
                    {
                        "name": "mongodb",
                        "port": 27017,
                    },
                    {
                        "name": "backup",
                        "port": 27018,
                    },
                    {
                        "name": "testing2",
                        "port": 27019,
                    },
                ],
            }
        },
    }

    return resource


@fixture(scope="module")
def disable_istio(
    multi_cluster_operator: Operator,
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    for mcc in member_cluster_clients:
        api = client.CoreV1Api(api_client=mcc.api_client)
        labels = {"istio-injection": "disabled"}
        ns = api.read_namespace(name=namespace)
        ns.metadata.labels.update(labels)
        api.replace_namespace(name=namespace, body=ns)
    return None


@fixture(scope="module")
def mongodb_multi(
    central_cluster_client: kubernetes.client.ApiClient,
    disable_istio,
    namespace: str,
    mongodb_multi_unmarshalled: MongoDB,
    multi_cluster_issuer_ca_configmap: str,
) -> MongoDB:
    mongodb_multi_unmarshalled["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "tls": {
            "ca": multi_cluster_issuer_ca_configmap,
        },
    }
    mongodb_multi_unmarshalled.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return mongodb_multi_unmarshalled.update()


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


@mark.e2e_mongodb_multi_cluster_tls_no_mesh
def test_update_coredns(cluster_clients: dict[str, kubernetes.client.ApiClient]):
    testhelper.test_update_coredns(cluster_clients)


@mark.e2e_mongodb_multi_cluster_tls_no_mesh
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@mark.e2e_mongodb_multi_cluster_tls_no_mesh
def test_create_mongodb_multi(
    mongodb_multi: MongoDB,
    namespace: str,
    server_certs: str,
    multi_cluster_issuer_ca_configmap: str,
    member_cluster_clients: List[MultiClusterClient],
    member_cluster_names: List[str],
):
    testhelper.test_create_mongodb_multi(
        mongodb_multi,
        namespace,
        server_certs,
        multi_cluster_issuer_ca_configmap,
        member_cluster_clients,
        member_cluster_names,
    )


@mark.e2e_mongodb_multi_cluster_tls_no_mesh
def test_service_overrides(
    namespace: str,
    mongodb_multi: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_service_overrides(namespace, mongodb_multi, member_cluster_clients)


@mark.e2e_mongodb_multi_cluster_tls_no_mesh
def test_placeholders_in_external_services(
    namespace: str,
    mongodb_multi: MongoDB,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_placeholders_in_external_services(namespace, mongodb_multi, member_cluster_clients)
