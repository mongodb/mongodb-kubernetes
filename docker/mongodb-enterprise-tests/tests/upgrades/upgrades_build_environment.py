"""
This is a multi stage test. Referenced on .evergreen.yml as e2e_operator_upgrade_multiple_clusters_allowed.

The test consist on upgrading the operator from version 1.2.2 to latest. Version 1.2.2 allows for
multiple clusters per project. After moving to latest version of the operator, the different resources
are meant to fail or to print warnings (depending on the version).

Stage 1 (this): e2e_operator_upgrade_build_deployment
Stage 2: e2e_operator_upgrade_scale_and_verify_deployment
"""

from kubetester.kubetester import skip_if_local, fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from pytest import fixture, mark


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), namespace=namespace)
    return resource.create()


@fixture(scope="module")
def sharded_cluster(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster.yaml"), namespace=namespace
    )
    return resource.create()


@mark.e2e_op_upgrade_one_deployment_first
def test_replica_set_reaches_running_phase(replica_set):
    replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    assert "warnings" not in replica_set["status"]


@skip_if_local
@mark.e2e_op_upgrade_one_deployment_first
def test_replica_set_client_can_connect_to_mongodb(replica_set):
    replica_set.assert_connectivity()


@mark.e2e_op_upgrade_one_deployment_first
def test_cluster_reaches_running_phase(sharded_cluster):
    sharded_cluster.assert_reaches_phase(Phase.Running, timeout=600)

    assert "warnings" not in sharded_cluster["status"]


@skip_if_local
@mark.e2e_op_upgrade_one_deployment_first
def test_cluster_client_can_connect_to_mongodb(sharded_cluster):
    sharded_cluster.assert_connectivity()
