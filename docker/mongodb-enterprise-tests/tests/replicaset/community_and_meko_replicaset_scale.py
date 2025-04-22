from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_community import MongoDBCommunity
from kubetester.operator import Operator
from pytest import fixture, mark
from tests import test_logger

logger = test_logger.get_test_logger(__name__)


@fixture(scope="function")
def mco_replica_set(namespace: str) -> MongoDBCommunity:
    resource = MongoDBCommunity.from_yaml(
        yaml_fixture("community-replicaset-simple.yaml"),
        namespace=namespace,
        name="mco-replica-set",
    )

    if try_load(resource):
        return resource

    return resource.update()


@fixture(scope="module")
def meko_replica_set(namespace: str, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-basic.yaml"), namespace=namespace, name="meko-replica-set")
    resource.set_version(custom_mdb_version)

    if try_load(resource):
        return resource

    return resource.update()


@mark.e2e_community_and_meko_replicaset_scale
def test_install_operator(community_operator: Operator):
    community_operator.assert_is_running()


@mark.e2e_community_and_meko_replicaset_scale
def test_install_secret(namespace: str):
    create_or_update_secret(namespace=namespace, name="my-user-password", data={"password": "<PASSWORD>"})


@mark.e2e_community_and_meko_replicaset_scale
def test_replicaset_running(mco_replica_set: MongoDBCommunity):
    mco_replica_set.assert_reaches_phase(Phase.Running, timeout=300)


@mark.e2e_community_and_meko_replicaset_scale
def test_meko_replicaset_running(meko_replica_set: MongoDB):
    meko_replica_set.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_community_and_meko_replicaset_scale
def test_replicaset_scale_up(mco_replica_set: MongoDBCommunity):
    rs = mco_replica_set.load()
    rs["spec"]["members"] = 5
    rs.update()
    # TODO: MCK As we don't have "observedGeneration" in MongoDBCommunity status, we could be checking the status too early.
    # We always need to check for abandoning phase first
    mco_replica_set.assert_abandons_phase(Phase.Running, timeout=60)
    mco_replica_set.assert_reaches_phase(Phase.Running, timeout=350)


@mark.e2e_community_and_meko_replicaset_scale
def test_replicaset_scale_down(mco_replica_set: MongoDBCommunity):
    rs = mco_replica_set.load()
    rs["spec"]["members"] = 3
    rs.update()
    mco_replica_set.assert_abandons_phase(Phase.Running, timeout=60)
    mco_replica_set.assert_reaches_phase(Phase.Running, timeout=350)


@mark.e2e_community_and_meko_replicaset_scale
def test_meko_replicaset_scale_up(meko_replica_set: MongoDB):
    rs = meko_replica_set.load()
    rs["spec"]["members"] = 5
    rs.update()
    meko_replica_set.assert_reaches_phase(Phase.Running, timeout=500)


@mark.e2e_community_and_meko_replicaset_scale
def test_meko_replicaset_scale_down(meko_replica_set: MongoDB):
    rs = meko_replica_set.load()
    rs["spec"]["members"] = 3
    rs.update()
    meko_replica_set.assert_reaches_phase(Phase.Running, timeout=1000)
