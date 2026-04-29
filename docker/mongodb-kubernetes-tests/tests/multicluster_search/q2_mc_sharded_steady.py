"""
Q2-MC MongoDBSearch e2e scaffold: multi-cluster, sharded source, NEW shape.

Deploys a MongoDBSearch across len(member_cluster_clients) clusters with
spec.clusters[] populated one-per-cluster, each entry pinning
syncSourceSelector.matchTags.region to a different region label, against an
external sharded source (router + 2 shards).

Asserts:
- spec.loadBalancer.managed.externalHostname carries `{clusterName}` AND
  `{shardName}` placeholders
- status.clusterStatusList.clusterStatuses[i].phase == "Running" for each cluster
- per-cluster, per-shard:
  status.clusterStatusList.clusterStatuses[i].loadBalancer.shards[<shardName>].phase
  == "Running"
"""

from typing import List

from kubetester import find_fixture
from kubetester.mongodb_search import MongoDBSearch
from kubetester.multicluster_client import MultiClusterClient
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.search.sharded_search_helper import build_external_sharded_source
from tests.multicluster_search.helpers import assert_envoy_ready_in_each_cluster, build_clusters_spec

MDB_RESOURCE_NAME = "mdb-sh-q2-mc"
MDBS_RESOURCE_NAME = "mdb-sh-q2-mc-search"

SHARD_COUNT = 2
MONGODS_PER_SHARD = 1
MONGOS_COUNT = 1


@fixture(scope="function")
def mdbs(namespace: str, member_cluster_clients: List[MultiClusterClient]) -> MongoDBSearch:
    resource = MongoDBSearch.from_yaml(
        find_fixture("q2-mc-sharded.yaml"),
        namespace=namespace,
        name=MDBS_RESOURCE_NAME,
    )
    resource["spec"]["source"] = {
        "username": "search-sync-source",
        "passwordSecretRef": {
            "name": f"{MDBS_RESOURCE_NAME}-search-sync-source-password",
            "key": "password",
        },
        "external": build_external_sharded_source(
            MDB_RESOURCE_NAME, namespace, MONGOS_COUNT, SHARD_COUNT, MONGODS_PER_SHARD
        ),
    }
    resource["spec"]["clusters"] = build_clusters_spec(member_cluster_clients)
    return resource


@mark.e2e_search_q2_mc_sharded_steady
def test_install_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_search_q2_mc_sharded_steady
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    mdbs.assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_search_q2_mc_sharded_steady
def test_verify_per_cluster_status(mdbs: MongoDBSearch, member_cluster_clients: List[MultiClusterClient]):
    mdbs.load()
    cluster_statuses = mdbs["status"]["clusterStatusList"]["clusterStatuses"]
    assert len(cluster_statuses) == len(
        member_cluster_clients
    ), f"expected {len(member_cluster_clients)} cluster status entries, got {len(cluster_statuses)}"

    expected_shard_names = {f"{MDB_RESOURCE_NAME}-{i}" for i in range(SHARD_COUNT)}
    for cs in cluster_statuses:
        assert cs["phase"] == "Running", f"cluster {cs['clusterName']}: phase={cs['phase']}, expected Running"
        shard_lb = cs["loadBalancer"]["shards"]
        assert (
            set(shard_lb.keys()) == expected_shard_names
        ), f"cluster {cs['clusterName']}: shard set mismatch — got {set(shard_lb.keys())}, want {expected_shard_names}"
        for shard_name, shard_status in shard_lb.items():
            phase = shard_status["phase"]
            assert (
                phase == "Running"
            ), f"cluster {cs['clusterName']}, shard {shard_name}: phase={phase}, expected Running"


@mark.e2e_search_q2_mc_sharded_steady
def test_verify_per_cluster_envoy_deployment(namespace: str, member_cluster_clients: List[MultiClusterClient]):
    assert_envoy_ready_in_each_cluster(namespace, MDBS_RESOURCE_NAME, member_cluster_clients)


@mark.e2e_search_q2_mc_sharded_steady
def test_verify_lb_status(mdbs: MongoDBSearch):
    mdbs.load()
    mdbs.assert_lb_status()
