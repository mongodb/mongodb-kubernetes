import re

from kubetester import try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_member_cluster_clients_using_cluster_mapping,
)

"""
This test checks the 'status.resourcesNotReady' element during sharded cluster reconciliation. It's expected to 
be populated with the information about current StatefulSet pending in the following order: config server, shard 0, 
shard 1, mongos.
"""


@fixture(scope="function")
def sc(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-single.yaml"),
        namespace=namespace,
        name="sharded-cluster-status",
    )

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    resource["spec"]["shardCount"] = 2

    if is_multi_cluster():
        enable_multi_cluster_deployment(
            resource=resource,
            shard_members_array=[1, 1, 1],
            mongos_members_array=[1, 1, None],
            configsrv_members_array=[None, 1, 1],
        )

    return resource.update()


@mark.e2e_sharded_cluster_statefulset_status
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_sharded_cluster_statefulset_status
def test_config_srv_reaches_pending_phase(sc: MongoDB):
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
        if sc.config_srv_members_in_cluster(cluster_member_client.cluster_name) > 0:
            cluster_idx = cluster_member_client.cluster_index
            configsrv_sts_name = sc.config_srv_statefulset_name(cluster_idx)
            cluster_reaches_not_ready(sc, configsrv_sts_name)


@mark.e2e_sharded_cluster_statefulset_status
def test_first_shard_reaches_pending_phase(sc: MongoDB):
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
        if sc.shard_members_in_cluster(cluster_member_client.cluster_name) > 0:
            cluster_idx = cluster_member_client.cluster_index
            shard0_sts_name = sc.shard_statefulset_name(0, cluster_idx)
            cluster_reaches_not_ready(sc, shard0_sts_name)


@mark.e2e_sharded_cluster_statefulset_status
def test_second_shard_reaches_pending_phase(sc: MongoDB):
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
        if sc.shard_members_in_cluster(cluster_member_client.cluster_name) > 0:
            cluster_idx = cluster_member_client.cluster_index
            shard1_sts_name = sc.shard_statefulset_name(1, cluster_idx)
            cluster_reaches_not_ready(sc, shard1_sts_name)


@mark.e2e_sharded_cluster_statefulset_status
def test_mongos_reaches_pending_phase(sc: MongoDB):
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(sc.name, sc.namespace):
        if sc.mongos_members_in_cluster(cluster_member_client.cluster_name) > 0:
            cluster_idx = cluster_member_client.cluster_index
            mongos_sts_name = sc.mongos_statefulset_name(cluster_idx)
            cluster_reaches_not_ready(sc, mongos_sts_name)


@mark.e2e_sharded_cluster_statefulset_status
def test_sharded_cluster_reaches_running_phase(sc: MongoDB):
    sc.assert_reaches_phase(Phase.Running, timeout=1000)
    assert sc.get_status_resources_not_ready() is None


def cluster_reaches_not_ready(sc: MongoDB, sts_name: str):
    """This function waits until the sharded cluster status gets 'resource_not_ready' element for the specified
    StatefulSet"""

    def resource_not_ready(s: MongoDB) -> bool:
        if s.get_status_resources_not_ready() is None:
            return False

        for idx, resource in enumerate(s.get_status_resources_not_ready()):
            if resource["name"] == sts_name:
                assert resource["kind"] == "StatefulSet"
                assert re.search("Not all the Pods are ready \(wanted: 1.*\)", resource["message"]) is not None

                return True

        return False

    sc.wait_for(resource_not_ready, timeout=150, should_raise=True)

    assert sc.get_status_phase() == Phase.Pending
