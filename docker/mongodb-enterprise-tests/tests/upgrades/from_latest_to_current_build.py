"""
This is a multi stage test. Referenced on .evergreen.yml as e2e_operator_upgrade_from_previous

The test consist on upgrading the operator from latest released version to current. Makes sure
that after an update, the MongoDB resources, which has been updated and rolling-restarted, go
back to Running state.

Stage 1 (this): e2e_latest_to_current_build
Stage 2: e2e_latest_to_current_verify
"""

from kubetester.kubetester import skip_if_local, fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from pytest import fixture, mark


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:

    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-upgrade.yaml"), "my-replica-set", namespace
    )
    resource.create()

    return resource


@mark.e2e_op_upgrade_replica_set_first
def test_reaches_running_phase(replica_set: MongoDB):
    replica_set.assert_reaches_phase("Running")


@skip_if_local
@mark.e2e_op_upgrade_replica_set_first
def test_client_can_connect_to_mongodb(replica_set: MongoDB):
    replica_set.assert_connectivity()
