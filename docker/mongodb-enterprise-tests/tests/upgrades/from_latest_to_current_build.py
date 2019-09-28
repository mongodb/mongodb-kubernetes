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


@mark.e2e_latest_to_current_build
def test_reaches_running_phase(replica_set):
    replica_set.reaches_phase("Running")
    assert replica_set["status"]["phase"] == "Running"


@skip_if_local
@mark.e2e_latest_to_current_build
def test_client_can_connect_to_mongodb(replica_set):
    replica_set.assert_connectivity()
