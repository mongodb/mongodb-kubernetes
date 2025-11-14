from typing import Dict, List

import kubernetes
import pytest
from kubetester.certs_mongodb_multi import create_multi_cluster_mongodb_tls_certs
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from tests.multicluster.conftest import cluster_spec_list

from ..shared import multi_2_cluster_replicaset as testhelper

CERT_SECRET_PREFIX = "clustercert"
MDB_RESOURCE = "multi-cluster-replica-set"
BUNDLE_SECRET_NAME = f"{CERT_SECRET_PREFIX}-{MDB_RESOURCE}-cert"


@pytest.fixture(scope="module")
def mongodb_multi_unmarshalled(
    namespace: str, member_cluster_names: List[str], custom_mdb_version: str
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodbmulticluster-multi.yaml"), MDB_RESOURCE, namespace)
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1])
    resource.set_version(ensure_ent_version(custom_mdb_version))
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


@pytest.mark.e2e_mogodbmulticluster_multi_cluster_2_clusters_replica_set
def test_create_kube_config_file(cluster_clients: Dict, member_cluster_names: List[str]):
    testhelper.test_create_kube_config_file(cluster_clients, member_cluster_names)


@pytest.mark.e2e_mogodbmulticluster_multi_cluster_2_clusters_replica_set
def test_deploy_operator(multi_cluster_operator: Operator):
    testhelper.test_deploy_operator(multi_cluster_operator)


@pytest.mark.e2e_mogodbmulticluster_multi_cluster_2_clusters_replica_set
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    testhelper.test_create_mongodb_multi(mongodb_multi)


@pytest.mark.e2e_mogodbmulticluster_multi_cluster_2_clusters_replica_set
def test_statefulset_is_created_across_multiple_clusters(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    testhelper.test_statefulset_is_created_across_multiple_clusters(mongodb_multi, member_cluster_clients)


@skip_if_local
@pytest.mark.e2e_mogodbmulticluster_multi_cluster_2_clusters_replica_set
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti, ca_path: str):
    testhelper.test_replica_set_is_reachable(mongodb_multi, ca_path)
