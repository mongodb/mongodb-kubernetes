from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.operator import Operator
from pytest import fixture, mark


@fixture(scope="module")
def sharded_cluster(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-single.yaml"),
        namespace=namespace,
        name="sharded-cluster-status",
    )
    resource.set_version(custom_mdb_version)
    resource["spec"]["shardCount"] = 2
    return resource.create()


"""
This test checks the 'status.resourcesNotReady' element during sharded cluster reconciliation. It's expected to 
be populated with the information about current StatefulSet pending in the following order: config server, shard 0, 
shard 1, mongos.
"""


@mark.e2e_sharded_cluster_statefulset_status
def test_install_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_sharded_cluster_statefulset_status
def test_config_srv_reaches_pending_phase(sharded_cluster: MongoDB):
    cluster_reaches_not_ready(sharded_cluster, sharded_cluster.name + "-config")


@mark.e2e_sharded_cluster_statefulset_status
def test_first_shard_reaches_pending_phase(sharded_cluster: MongoDB):
    cluster_reaches_not_ready(sharded_cluster, sharded_cluster.name + "-0")


@mark.e2e_sharded_cluster_statefulset_status
def test_second_shard_reaches_pending_phase(sharded_cluster: MongoDB):
    cluster_reaches_not_ready(sharded_cluster, sharded_cluster.name + "-1")


@mark.e2e_sharded_cluster_statefulset_status
def test_mongos_reaches_pending_phase(sharded_cluster: MongoDB):
    cluster_reaches_not_ready(sharded_cluster, sharded_cluster.name + "-mongos")


@mark.e2e_sharded_cluster_statefulset_status
def test_sharded_cluster_reaches_running_phase(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=100)
    assert sharded_cluster.get_status_resources_not_ready() is None


def cluster_reaches_not_ready(sharded_cluster: MongoDB, sts_name: str):
    """This function waits until the sharded cluster status gets 'resource_not_ready' element for the specified
    StatefulSet"""

    def resource_not_ready(s: MongoDB):
        if s.get_status_resources_not_ready() is None:
            return False
        return s.get_status_resources_not_ready()[0]["name"] == sts_name

    sharded_cluster.wait_for(resource_not_ready, timeout=150, should_raise=True)
    sharded_cluster.assert_status_resource_not_ready(
        sts_name,
        msg_regexp="Not all the Pods are ready \(wanted: 1.*\)",
    )
    assert sharded_cluster.get_status_phase() == Phase.Pending
