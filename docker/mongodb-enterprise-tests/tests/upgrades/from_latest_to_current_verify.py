"""
This is a multi stage test. Referenced on .evergreen.yml as e2e_operator_upgrade_from_previous

Stage 1: e2e_latest_to_current_build
Stage 2 (this): e2e_latest_to_current_verify
"""
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB, Phase
from pytest import fixture, mark


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    resource = MongoDB("my-replica-set", namespace).load()

    yield resource


@mark.e2e_op_upgrade_replica_set_second
def test_reaches_running_phase(replica_set):
    replica_set.assert_reaches_phase(Phase.Running)

    assert replica_set["metadata"]["name"] == replica_set.name
    assert replica_set["status"]["members"] == 3
    assert replica_set["status"]["version"] == "4.0.10"


@skip_if_local
@mark.e2e_op_upgrade_replica_set_second
def test_client_can_connect_to_mongodb(replica_set):
    replica_set.assert_connectivity()
