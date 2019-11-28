"""
This is a multi stage test. Referenced on .evergreen.yml as e2e_operator_upgrade_multiple_clusters_allowed.

This is stage 2 (verification): e2e_operator_upgrade_scale_and_verify_deployment
"""

from pytest import fixture, mark
from kubetester.mongodb import MongoDB


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    return MongoDB("my-replica-set", namespace).load()


@fixture(scope="module")
def sharded_cluster(namespace: str) -> MongoDB:
    return MongoDB("sh001-base", namespace).load()


@mark.e2e_operator_upgrade_scale_and_verify_deployment
def test_replica_set_gets_to_running_state_with_warnings(replica_set: MongoDB):
    replica_set.reaches_phase("Pending", timeout=600)
    assert replica_set["status"]["phase"] == "Pending"
    assert (
        replica_set["status"]["message"]
        == "Cannot have more than 1 MongoDB Cluster per project (see https://docs.mongodb.com/kubernetes-operator/stable/tutorial/migrate-to-single-resource/)"
    )


@mark.e2e_operator_upgrade_scale_and_verify_deployment
def test_sharded_cluster_gets_to_running_state_with_warnings(sharded_cluster: MongoDB):
    # Sharded clusters take a long time to restart in the Kops cluster
    sharded_cluster.reaches_phase("Pending", timeout=1800)

    sharded_cluster.reload()
    assert sharded_cluster["status"]["phase"] == "Pending"
    assert (
        sharded_cluster["status"]["message"]
        == "Cannot have more than 1 MongoDB Cluster per project (see https://docs.mongodb.com/kubernetes-operator/stable/tutorial/migrate-to-single-resource/)"
    )
