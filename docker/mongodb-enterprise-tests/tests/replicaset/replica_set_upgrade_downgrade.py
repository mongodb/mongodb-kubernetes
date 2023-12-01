import pymongo
from kubetester.kubetester import KubernetesTester
from kubetester.mongotester import (
    MongoDBBackgroundTester,
    MongoTester,
    ReplicaSetTester,
)
from pytest import fixture, mark

TEST_DATA = {"foo": "bar"}
TEST_DB = "testdb"
TEST_COLLECTION = "testcollection"


@fixture(scope="module")
def mongod_tester():
    return ReplicaSetTester("my-replica-set-downgrade", 3)


@fixture(scope="module")
def mdb_health_checker(mongod_tester: MongoTester) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(
        mongod_tester,
        allowed_sequential_failures=1,
        health_function_params={
            "attempts": 1,
            "write_concern": pymongo.WriteConcern(w="majority"),
        },
    )


@fixture
def mdb_test_collection(mongod_tester):
    collection = mongod_tester.client[TEST_DB]
    return collection[TEST_COLLECTION]


@mark.e2e_replica_set_upgrade_downgrade
class TestReplicaSetUpgradeDowngradeCreate(KubernetesTester):
    """
    name: ReplicaSet upgrade downgrade (create)
    description: |
      Creates a replica set, then upgrades it with the compatibility version set and then downgrades back
    create:
      file: replica-set-downgrade.yaml
      wait_until: in_running_state
      timeout: 300
    """

    def test_start_mongod_background_tester(self, mdb_health_checker):
        mdb_health_checker.start()

    def test_db_connectable(self, mongod_tester):
        mongod_tester.assert_version("4.4.2")

    def test_insert_test_data(self, mdb_test_collection):
        mdb_test_collection.insert_one(TEST_DATA)


@mark.e2e_replica_set_upgrade_downgrade
class TestReplicaSetUpgradeDowngradeUpdate(KubernetesTester):
    """
    name: ReplicaSet upgrade downgrade (update)
    description: |
      Updates a ReplicaSet to bigger version, leaving feature compatibility version as it was
    update:
      file: replica-set-downgrade.yaml
      patch: '[{"op":"replace","path":"/spec/version", "value": "4.4.0"}, {"op":"add","path":"/spec/featureCompatibilityVersion", "value": "4.4"}]'
      wait_until: in_running_state
      timeout: 300
    """

    def test_db_connectable(self, mongod_tester):
        mongod_tester.assert_version("4.4.0")


@mark.e2e_replica_set_upgrade_downgrade
class TestReplicaSetUpgradeDowngradeRevert(KubernetesTester):
    """
    name: ReplicaSet upgrade downgrade (downgrade)
    description: |
      Updates a ReplicaSet to the same version it was created initially
    update:
      file: replica-set-downgrade.yaml
      wait_until: in_running_state
      timeout: 300
    """

    def test_db_connectable(self, mongod_tester):
        mongod_tester.assert_version("4.4.2")

    def test_mdb_healthy_throughout_change_version(self, mdb_health_checker):
        mdb_health_checker.assert_healthiness()

    def test_data_exists(self, mdb_test_collection):
        assert mdb_test_collection.estimated_document_count() == 1
