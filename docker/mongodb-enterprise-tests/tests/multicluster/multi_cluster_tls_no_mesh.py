from typing import List

import kubernetes
from kubernetes import client
from pytest import mark, fixture

from kubetester import create_or_update
from kubetester.certs import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from tests.multicluster.conftest import cluster_spec_list

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"
BUNDLE_PEM_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert-pem"


@fixture(scope="module")
def mongodb_multi_unmarshalled(namespace: str, member_cluster_names: List[str]) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace
    )
    resource["spec"]["persistent"] = False
    # These domains map 1:1 to the CoreDNS file. Please be mindful when updating them.
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [1, 1, 1])

    resource["spec"]["externalAccess"] = {}
    resource["spec"]["clusterSpecList"][0]["externalAccess"] = {
        "externalDomain": "kind-e2e-cluster-1.interconnected",
        "externalService": {
            "spec": {
                "type": "LoadBalancer",
                "loadBalancerIP": "172.18.255.211",
                "publishNotReadyAddresses": True,
                "ports": [
                    {
                        "name": "mongodb",
                        "port": 27017,
                    },
                    {
                        "name": "backup",
                        "port": 27018,
                    }
                ]
            }
        }
    }
    resource["spec"]["clusterSpecList"][1]["externalAccess"] = {
        "externalDomain": "kind-e2e-cluster-2.interconnected",
        "externalService": {
            "spec": {
                "type": "LoadBalancer",
                "loadBalancerIP": "172.18.255.221",
                "publishNotReadyAddresses": True,
                "ports": [
                    {
                        "name": "mongodb",
                        "port": 27017,
                    },
                    {
                        "name": "backup",
                        "port": 27018,
                    }
                ]
            }
        }
    }
    resource["spec"]["clusterSpecList"][2]["externalAccess"] = {
        "externalDomain": "kind-e2e-cluster-3.interconnected",
        "externalService": {
            "spec": {
                "type": "LoadBalancer",
                "loadBalancerIP": "172.18.255.231",
                "publishNotReadyAddresses": True,
                "ports": [
                    {
                        "name": "mongodb",
                        "port": 27017,
                    },
                    {
                        "name": "backup",
                        "port": 27018,
                    }
                ]
            }
        }
    }

    return resource


@fixture(scope="function")
def disable_istio(multi_cluster_operator: Operator, namespace: str, member_cluster_clients: List[MultiClusterClient]) -> str:
    for mcc in member_cluster_clients:
        api = client.CoreV1Api(api_client=mcc.api_client)
        labels = {"istio-injection": "disabled"}
        ns = api.read_namespace(name=namespace)
        ns.metadata.labels.update(labels)
        api.replace_namespace(name=namespace, body=ns)
    return None


@fixture(scope="function")
def mongodb_multi(
        central_cluster_client: kubernetes.client.ApiClient,
        disable_istio: str,
        namespace: str,
        mongodb_multi_unmarshalled: MongoDBMulti,
        multi_cluster_issuer_ca_configmap: str,
) -> MongoDBMulti:
    mongodb_multi_unmarshalled["spec"]["security"] = {
        "certsSecretPrefix": CERT_SECRET_PREFIX,
        "tls": {
            "ca": multi_cluster_issuer_ca_configmap,
        },
    }
    mongodb_multi_unmarshalled.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    return create_or_update(mongodb_multi_unmarshalled)


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


@mark.e2e_multi_cluster_tls_no_mesh
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_tls_no_mesh
def test_create_mongodb_multi(
        mongodb_multi: MongoDBMulti,
        namespace: str,
        server_certs: str,
        multi_cluster_issuer_ca_configmap: str,
        member_cluster_clients: List[MultiClusterClient],
        member_cluster_names: List[str]
):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=2400)
