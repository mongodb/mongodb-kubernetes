"""
Q2-MC MongoDBSearch e2e scaffold: multi-cluster, ReplicaSet source, NEW shape.

Deploys a MongoDBSearch across len(member_cluster_clients) clusters with
spec.clusters[] populated one-per-cluster, each entry pinning
syncSourceSelector.matchTags.region to a different region label.

Asserts:
- spec.loadBalancer.managed.externalHostname carries `{clusterName}` and is
  templated per cluster
- status.clusterStatusList.clusterStatuses[i].phase == "Running" for each cluster
- the operator-managed Envoy Deployment in *each* cluster reports
  status.availableReplicas >= 1
"""

from typing import List

from kubetester import find_fixture
from kubetester.mongodb_search import MongoDBSearch
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.multicluster_search.helpers import assert_envoy_ready_in_each_cluster, build_clusters_spec

MDB_RESOURCE_NAME = "mdb-rs-q2-mc"
MDBS_RESOURCE_NAME = "mdb-rs-q2-mc-search"


@fixture(scope="function")
def mdbs(namespace: str, member_cluster_clients: List[MultiClusterClient]) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        find_fixture("q2-mc-rs.yaml"),
        namespace=namespace,
        name=MDBS_RESOURCE_NAME,
    )
    resource["spec"]["source"] = {"mongodbResourceRef": {"name": MDB_RESOURCE_NAME}}
    resource["spec"]["clusters"] = build_clusters_spec(member_cluster_clients)
    return resource


@mark.e2e_search_q2_mc_rs_steady
def test_install_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_search_q2_mc_rs_steady
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_q2_mc_rs_steady
def test_verify_per_cluster_status(mdbs: MongoDBSearch, member_cluster_clients: List[MultiClusterClient]):
    mdbs.load()
    cluster_statuses = mdbs["status"]["clusterStatusList"]["clusterStatuses"]
    assert len(cluster_statuses) == len(member_cluster_clients), (
        f"expected {len(member_cluster_clients)} cluster status entries, got {len(cluster_statuses)}"
    )
    cluster_names = {mcc.cluster_name for mcc in member_cluster_clients}
    for cs in cluster_statuses:
        assert cs["clusterName"] in cluster_names, f"unexpected clusterName: {cs['clusterName']}"
        assert cs["phase"] == "Running", f"cluster {cs['clusterName']}: phase={cs['phase']}, expected Running"


@mark.e2e_search_q2_mc_rs_steady
def test_verify_per_cluster_envoy_deployment(
    namespace: str, member_cluster_clients: List[MultiClusterClient]
):
    assert_envoy_ready_in_each_cluster(namespace, MDBS_RESOURCE_NAME, member_cluster_clients)


@mark.e2e_search_q2_mc_rs_steady
def test_verify_lb_status(mdbs: MongoDBSearch):
    mdbs.load()
    mdbs.assert_lb_status()
