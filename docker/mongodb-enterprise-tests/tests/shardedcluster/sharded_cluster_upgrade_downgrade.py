import pymongo
from kubetester.kubetester import KubernetesTester
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
def mdb_health_checker(mongod_tester: MongoTester) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(
        mongod_tester,
        # After running multiple tests, it seems that on sharded_cluster version changes we have more sequential errors.
        allowed_sequential_failures=3,
        health_function_params={
            "attempts": 1,
            "write_concern": pymongo.WriteConcern(w="majority"),
        },
    )


@mark.e2e_sharded_cluster_upgrade_downgrade
class TestShardedClusterUpgradeDowngradeCreate(KubernetesTester):
    """
    name: ShardedCluster upgrade downgrade (create)
    description: |
      Creates a sharded cluster, then upgrades it with compatibility version set and then downgrades back
    create:
      file: sharded-cluster-downgrade.yaml
      wait_until: in_running_state
      timeout: 1000
    """

    def test_start_mongod_background_tester(self, mdb_health_checker):
        mdb_health_checker.start()

    def test_db_connectable(self, mongod_tester):
        mongod_tester.assert_connectivity()
        mongod_tester.assert_version("4.4.2")


@mark.e2e_sharded_cluster_upgrade_downgrade
class TestShardedClusterUpgradeDowngradeUpdate(KubernetesTester):
    """
    name: ShardedCluster upgrade downgrade (update)
    description: |
      Updates a ShardedCluster to bigger version, leaving feature compatibility version as it was
    update:
      file: sharded-cluster-downgrade.yaml
      patch: '[{"op":"replace","path":"/spec/version", "value": "4.4.0"}, {"op":"add","path":"/spec/featureCompatibilityVersion", "value": "4.4"}]'
      wait_until: in_running_state
      timeout: 1000
    """

    def test_db_connectable(self, mongod_tester):
        mongod_tester.assert_version("4.4.0")


@mark.e2e_sharded_cluster_upgrade_downgrade
class TestShardedClusterUpgradeDowngradeRevert(KubernetesTester):
    """
    name: ShardedCluster upgrade downgrade (downgrade)
    description: |
      Updates a ShardedCluster to the same version it was created initially
    update:
      file: sharded-cluster-downgrade.yaml
      wait_until: in_running_state
      timeout: 1000
    """

    def test_db_connectable(self, mongod_tester):
        mongod_tester.assert_version("4.4.2")

    def test_mdb_healthy_throughout_change_version(self, mdb_health_checker):
        mdb_health_checker.assert_healthiness()
