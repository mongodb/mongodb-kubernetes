import pymongo
from kubetester import MongoDB, create_or_update, try_load
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.mongotester import (
    MongoDBBackgroundTester,
    MongoTester,
    ShardedClusterTester,
)
from pytest import fixture, mark


@fixture(scope="module")
def mongod_tester():
    return ShardedClusterTester("sh001-downgrade", 1)


@fixture(scope="module")
def sharded_cluster(namespace: str, custom_mdb_prev_version: str, cluster_domain: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("sharded-cluster-downgrade.yaml"), namespace=namespace)
    resource.set_version(custom_mdb_prev_version)
    if try_load(resource):
        return resource
    return create_or_update(resource)


@fixture(scope="module")
def mdb_health_checker(mongod_tester: MongoTester) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(
        mongod_tester,
        # After running multiple tests, it seems that on sharded_cluster version changes we have more sequential errors.
        allowed_sequential_failures=5,
        health_function_params={
            "attempts": 1,
            "write_concern": pymongo.WriteConcern(w="majority"),
        },
    )


@mark.e2e_sharded_cluster_upgrade_downgrade
class TestShardedClusterUpgradeDowngradeCreate(KubernetesTester):

    def test_mdb_created(self, sharded_cluster: MongoDB):
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1000)

    def test_start_mongod_background_tester(self, mdb_health_checker):
        mdb_health_checker.start()

    def test_db_connectable(self, mongod_tester, custom_mdb_prev_version: str):
        mongod_tester.assert_connectivity()
        mongod_tester.assert_version(custom_mdb_prev_version)


@mark.e2e_sharded_cluster_upgrade_downgrade
class TestShardedClusterUpgradeDowngradeUpdate(KubernetesTester):

    def test_mongodb_upgrade(self, sharded_cluster: MongoDB, custom_mdb_version: str, custom_mdb_prev_version: str):
        sharded_cluster.load()
        sharded_cluster.set_version(custom_mdb_version)
        fcv = fcv_from_version(custom_mdb_prev_version)
        sharded_cluster["spec"]["featureCompatibilityVersion"] = fcv
        create_or_update(sharded_cluster)
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)
        sharded_cluster.tester().assert_version(custom_mdb_version)

    def test_db_connectable(self, mongod_tester, custom_mdb_version: str):
        mongod_tester.assert_version(custom_mdb_version)


@mark.e2e_sharded_cluster_upgrade_downgrade
class TestShardedClusterUpgradeDowngradeRevert(KubernetesTester):

    def test_mongodb_downgrade(self, sharded_cluster: MongoDB, custom_mdb_prev_version: str):
        sharded_cluster.load()
        sharded_cluster.set_version(custom_mdb_prev_version)
        create_or_update(sharded_cluster)
        sharded_cluster.assert_reaches_phase(Phase.Running, timeout=1200)
        sharded_cluster.tester().assert_version(custom_mdb_prev_version)

    def test_db_connectable(self, mongod_tester, custom_mdb_prev_version):
        mongod_tester.assert_version(custom_mdb_prev_version)

    def test_mdb_healthy_throughout_change_version(self, mdb_health_checker):
        mdb_health_checker.assert_healthiness()
