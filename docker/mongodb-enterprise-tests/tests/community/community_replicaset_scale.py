from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.operator import Operator
from pytest import fixture, mark
from tests import test_logger

logger = test_logger.get_test_logger(__name__)


@fixture(scope="function")
def mdbc(namespace: str) -> MongoDBCommunity:
    resource = MongoDBCommunity.from_yaml(
        yaml_fixture("community-replicaset-simple.yaml"),
        namespace=namespace,
    )

    if try_load(resource):
        return resource

    return resource.update()


@mark.e2e_community_replicaset_scale
def test_install_operator(community_operator: Operator):
    community_operator.assert_is_running()


@mark.e2e_community_replicaset_scale
def test_install_secret(namespace: str):
    create_or_update_secret(namespace=namespace, name="my-user-password", data={"password": "<PASSWORD>"})


@mark.e2e_community_replicaset_scale
def test_replicaset_running(mdbc: MongoDBCommunity):
    mdbc.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_community_replicaset_scale
def test_replicaset_scale_up(mdbc: MongoDBCommunity):
    rs = mdbc.load()
    rs["spec"]["members"] = 5
    rs.update()
    # TODO: MCK As we don't have "observedGeneration" in MongoDBCommunity status, we could be checking the status too early.
    # We always need to check for abandoning phase first
    mdbc.assert_abandons_phase(Phase.Running, timeout=60)
    mdbc.assert_reaches_phase(Phase.Running, timeout=350)


@mark.e2e_community_replicaset_scale
def test_replicaset_scale_down(mdbc: MongoDBCommunity):
    rs = mdbc.load()
    rs["spec"]["members"] = 3
    rs.update()
    mdbc.assert_abandons_phase(Phase.Running, timeout=60)
    mdbc.assert_reaches_phase(Phase.Running, timeout=350)
