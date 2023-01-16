from typing import Dict, List

import kubernetes
import pytest
from kubetester.certs import create_multi_cluster_mongodb_tls_certs, Certificate
from kubetester.mongodb import Phase
from kubetester.mongodb_multi import MongoDBMulti, MultiClusterClient
from kubetester.operator import Operator
from kubetester.kubetester import fixture as yaml_fixture, skip_if_local
from kubetester.mongotester import with_tls

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"


@pytest.fixture(scope="module")
def mongodb_multi_unmarshalled(namespace: str) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace
    )
    resource["spec"]["clusterSpecList"] = [
        {"clusterName": "e2e.cluster1.mongokubernetes.com", "members": 2},
        {"clusterName": "e2e.cluster2.mongokubernetes.com", "members": 1},
    ]
    return resource


@pytest.fixture(scope="module")
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


@pytest.fixture(scope="module")
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


@pytest.mark.e2e_multi_cluster_2_clusters_replica_set
def test_create_kube_config_file(cluster_clients: Dict):
    clients = cluster_clients

    assert len(clients) == 2
    assert "e2e.cluster1.mongokubernetes.com" in clients
    assert "e2e.cluster2.mongokubernetes.com" in clients


@pytest.mark.e2e_multi_cluster_2_clusters_replica_set
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@pytest.mark.e2e_multi_cluster_2_clusters_replica_set
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_multi_cluster_2_clusters_replica_set
def test_statefulset_is_created_across_multiple_clusters(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)
    cluster_one_client = member_cluster_clients[0]
    cluster_one_sts = statefulsets[cluster_one_client.cluster_name]
    assert cluster_one_sts.status.ready_replicas == 2

    cluster_two_client = member_cluster_clients[1]
    cluster_two_sts = statefulsets[cluster_two_client.cluster_name]
    assert cluster_two_sts.status.ready_replicas == 1


@skip_if_local
@pytest.mark.e2e_multi_cluster_2_clusters_replica_set
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti, ca_path: str):
    tester = mongodb_multi.tester()
    tester.assert_connectivity(opts=[with_tls(use_tls=True, ca_path=ca_path)])
