from typing import List

import kubernetes
from kubernetes import client
from kubetester import create_or_update, get_service
from kubetester.certs import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.common.placeholders import placeholders
from tests.conftest import update_coredns_hosts
from tests.multicluster.conftest import cluster_spec_list

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"
BUNDLE_PEM_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert-pem"


@fixture(scope="module")
def mongodb_multi_unmarshalled(namespace: str, member_cluster_names: List[str]) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
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
def test_update_coredns(cluster_clients: dict[str, kubernetes.client.ApiClient]):
    hosts = [
        ("172.18.255.211", "test.kind-e2e-cluster-1.interconnected"),
        (
            "172.18.255.211",
            "multi-cluster-replica-set-0-0.kind-e2e-cluster-1.interconnected",
        ),
        (
            "172.18.255.212",
            "multi-cluster-replica-set-0-1.kind-e2e-cluster-1.interconnected",
        ),
        (
            "172.18.255.213",
            "multi-cluster-replica-set-0-2.kind-e2e-cluster-1.interconnected",
        ),
        ("172.18.255.221", "test.kind-e2e-cluster-2.interconnected"),
        (
            "172.18.255.221",
            "multi-cluster-replica-set-1-0.kind-e2e-cluster-2.interconnected",
        ),
        (
            "172.18.255.222",
            "multi-cluster-replica-set-1-1.kind-e2e-cluster-2.interconnected",
        ),
        (
            "172.18.255.223",
            "multi-cluster-replica-set-1-2.kind-e2e-cluster-2.interconnected",
        ),
        ("172.18.255.231", "test.kind-e2e-cluster-3.interconnected"),
        (
            "172.18.255.231",
            "multi-cluster-replica-set-2-0.kind-e2e-cluster-3.interconnected",
        ),
        (
            "172.18.255.232",
            "multi-cluster-replica-set-2-1.kind-e2e-cluster-3.interconnected",
        ),
        (
            "172.18.255.233",
            "multi-cluster-replica-set-2-2.kind-e2e-cluster-3.interconnected",
        ),
    ]

    for cluster_name, cluster_api in cluster_clients.items():
        update_coredns_hosts(hosts, cluster_name, api_client=cluster_api)


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
    member_cluster_names: List[str],
):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=2400, ignore_errors=True)


@mark.e2e_multi_cluster_tls_no_mesh
def test_service_overrides(
    namespace: str,
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    for cluster_idx, member_cluster_client in enumerate(member_cluster_clients):
        for pod_idx in range(0, 2):
            external_service_name = f"{mongodb_multi.name}-{cluster_idx}-{pod_idx}-svc-external"
            external_service = get_service(
                namespace,
                external_service_name,
                api_client=member_cluster_client.api_client,
            )

            assert external_service is not None
            assert external_service.spec.type == "LoadBalancer"
            assert external_service.spec.publish_not_ready_addresses
            ports = external_service.spec.ports
            assert len(ports) == 3
            assert ports[0].name == "mongodb"
            assert ports[0].target_port == 27017
            assert ports[0].port == 27017
            assert ports[1].name == "backup"
            assert ports[1].target_port == 27018
            assert ports[1].port == 27018
            assert ports[2].name == f"testing{cluster_idx}"
            assert ports[2].target_port == 27019
            assert ports[2].port == 27019


@mark.e2e_multi_cluster_tls_no_mesh
def test_placeholders_in_external_services(
    namespace: str,
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    for cluster_spec_item in mongodb_multi["spec"]["clusterSpecList"]:
        annotations = placeholders.get_annotations_with_placeholders_for_multi_cluster(
            prefix=f'{cluster_spec_item["clusterName"]},'
        )
        external_access = cluster_spec_item.get("externalAccess", {})
        external_service = external_access.get("externalService", {})
        external_service["annotations"] = annotations
        external_access["externalService"] = external_service
        cluster_spec_item["externalAccess"] = external_access

    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=300)

    external_domains = [
        "kind-e2e-cluster-1.interconnected",
        "kind-e2e-cluster-2.interconnected",
        "kind-e2e-cluster-3.interconnected",
    ]
    name = mongodb_multi["metadata"]["name"]
    for _, member_cluster_client in enumerate(member_cluster_clients):
        members = mongodb_multi.get_item_spec(member_cluster_client.cluster_name)["members"]
        for pod_idx in range(0, members):
            cluster_idx = member_cluster_client.cluster_index
            service = client.CoreV1Api(api_client=member_cluster_client.api_client).read_namespaced_service(
                f"{name}-{cluster_idx}-{pod_idx}-svc-external", namespace
            )
            cluster_name = member_cluster_client.cluster_name
            external_domain = external_domains[cluster_idx]
            expected_annotations = placeholders.get_expected_annotations_multi_cluster_no_mesh(
                name=name,
                namespace=namespace,
                pod_idx=pod_idx,
                external_domain=external_domain,
                cluster_index=cluster_idx,
                cluster_name=cluster_name,
                prefix=f"{cluster_name},",
            )
            assert service.metadata.annotations == expected_annotations
