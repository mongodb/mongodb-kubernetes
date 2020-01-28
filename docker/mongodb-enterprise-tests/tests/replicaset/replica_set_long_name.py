from pytest import fixture, mark
from kubetester.kubetester import skip_if_local, fixture as yaml_fixture
from kubetester.mongodb import MongoDB


@fixture(scope="module")
def replica_set(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-long-name.yaml"), namespace=namespace
    )
    return resource.create()


@mark.e2e_replica_set_long_name
def test_reaches_running_phase(replica_set: MongoDB):
    replica_set.assert_reaches_phase("Running")


@mark.e2e_replica_set_long_name
@skip_if_local
def test_replica_set_was_configured(replica_set):
    """
    Creates a Replica set with a long name and check that it works.

    At time of writing, 52 characters is the maximum length name that a
    MongoDB resource can have, as the operator will create dependent
    resources that have short name limits, usually by composing the MongoDB
    resource's name with a suffix.
    """
    replica_set.assert_connectivity()
