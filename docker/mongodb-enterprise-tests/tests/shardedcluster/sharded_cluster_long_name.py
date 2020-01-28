from pytest import fixture, mark
from kubetester.kubetester import skip_if_local, fixture as yaml_fixture
from kubetester.mongodb import MongoDB


@fixture(scope="module")
def sharded_cluster(namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("sharded-cluster-long-name.yaml"), namespace=namespace
    )
    return resource.create()


@mark.e2e_sharded_cluster_long_name
def test_reaches_running_phase(sharded_cluster: MongoDB):
    sharded_cluster.assert_reaches_phase("Running")


@mark.e2e_sharded_cluster_long_name
@skip_if_local
def test_sharded_cluster_was_configured(sharded_cluster):
    """
    Creates a sharded cluster with a long name and check that it works.

    At time of writing, 52 characters is the maximum length name that a
    MongoDB resource can have, as the operator will create dependent
    resources that have short name limits, usually by composing the MongoDB
    resource's name with a suffix.
    """
    sharded_cluster.assert_connectivity()
