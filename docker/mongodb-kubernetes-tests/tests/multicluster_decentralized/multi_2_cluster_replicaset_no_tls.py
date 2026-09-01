"""TLS-free fork of tests/multicluster/multi_2_cluster_replicaset.py (M7 Tier 2).

The original is blocked on TLS, which the decentralized POC guards against; stripping the
security block and the with_tls connectivity opts loses nothing this exercise measures — the
point is live coverage of the two-cluster topology invariant: a deployment spread across only 2
of the member clusters still reaches Running and stays reachable. Keep the test bodies diffable
against the original.
"""

from typing import Dict, List

import kubernetes
import pytest
from kubetester import try_load, wait_until
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from tests.multicluster.conftest import cluster_spec_list

MDB_RESOURCE = "multi-cluster-replica-set"


@pytest.fixture(scope="module")
def mongodb_multi(
    namespace: str,
    member_cluster_names: List[str],
    custom_mdb_version: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), MDB_RESOURCE, namespace)
    resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, [2, 1])
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    try_load(resource)
    return resource


@pytest.mark.e2e_multi_cluster_decentralized_2_clusters_replica_set
def test_create_kube_config_file(cluster_clients: Dict, member_cluster_names: List[str]):
    clients = cluster_clients

    assert len(clients) == 2
    assert member_cluster_names[0] in clients
    assert member_cluster_names[1] in clients


@pytest.mark.e2e_multi_cluster_decentralized_2_clusters_replica_set
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@pytest.mark.e2e_multi_cluster_decentralized_2_clusters_replica_set
def test_create_mongodb_multi(mongodb_multi: MongoDBMulti):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=1200)


@pytest.mark.e2e_multi_cluster_decentralized_2_clusters_replica_set
def test_statefulset_is_created_across_multiple_clusters(
    mongodb_multi: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    # Divergence from the original's immediate read: decentralized Running keys on the OM
    # witness (agents in goal state), not on pod readiness, so ready_replicas can lag the
    # phase flip by a probe period.
    def sts_ready() -> bool:
        statefulsets = mongodb_multi.read_statefulsets(member_cluster_clients)
        return (
            statefulsets[member_cluster_clients[0].cluster_name].status.ready_replicas == 2
            and statefulsets[member_cluster_clients[1].cluster_name].status.ready_replicas == 1
        )

    wait_until(sts_ready, timeout=120)


@skip_if_local
@pytest.mark.e2e_multi_cluster_decentralized_2_clusters_replica_set
def test_replica_set_is_reachable(mongodb_multi: MongoDBMulti):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()
