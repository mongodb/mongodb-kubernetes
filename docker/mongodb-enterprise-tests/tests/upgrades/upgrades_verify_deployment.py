"""
This is a multi stage test. Referenced on .evergreen.yml as e2e_operator_upgrade_multiple_clusters_allowed.

This is stage 2 (verification): e2e_operator_upgrade_scale_and_verify_deployment
"""

from kubetester.mongodb import MongoDB, Phase
from pytest import fixture, mark


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    return MongoDB("my-replica-set", namespace).load()


@fixture(scope="module")
def sharded_cluster(namespace: str) -> MongoDB:
    return MongoDB("sh001-base", namespace).load()


@mark.e2e_op_upgrade_one_deployment_second
def test_replica_set_gets_to_running_state_with_warnings(replica_set: MongoDB):
    replica_set.assert_reaches_phase(
        Phase.Pending,
        msg_regexp="Cannot have more than 1 MongoDB Cluster per project",
        timeout=200,
    )


@mark.e2e_op_upgrade_one_deployment_second
def test_sharded_cluster_gets_to_running_state_with_warnings(sharded_cluster: MongoDB):
    # Sharded clusters take a long time to restart in the Kops cluster
    sharded_cluster.assert_reaches_phase(
        Phase.Pending,
        msg_regexp="Cannot have more than 1 MongoDB Cluster per project",
        timeout=1800,
    )
