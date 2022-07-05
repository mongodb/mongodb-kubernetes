from pytest import fixture, mark
from kubetester.kubetester import KubernetesTester

# TODO change 3.6 -> 4.0 upgrade to 4.0 -> 4.2 when mongodb is released
from kubetester.mongotester import ReplicaSetTester


TEST_DATA = {"foo": "bar"}
TEST_DB = "testdb"
TEST_COLLECTION = "testcollection"


@fixture
def mongod_tester():
    return ReplicaSetTester("my-replica-set-downgrade", 3)


@fixture
def mdb_test_collection(mongod_tester):
    collection = mongod_tester.client[TEST_DB]
    return collection[TEST_COLLECTION]


@mark.e2e_replica_set_upgrade_downgrade
class TestReplicaSetUpgradeDowngradeCreate(KubernetesTester):
    """
    name: ReplicaSet upgrade downgrade (create)
    description: |
      Creates a replica set, then upgrades it with compatibility version set and then downgrades back
    create:
      file: replica-set-downgrade.yaml
      wait_until: in_running_state
      timeout: 300
    """

    def test_db_connectable(self, mongod_tester):
        mongod_tester.assert_version("3.6.20")

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
      patch: '[{"op":"replace","path":"/spec/version", "value": "4.0.15"}, {"op":"add","path":"/spec/featureCompatibilityVersion", "value": "3.6"}]'
      wait_until: in_running_state
      timeout: 300
    """

    def test_db_connectable(self, mongod_tester):
        mongod_tester.assert_version("4.0.15")


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
        mongod_tester.assert_version("3.6.20")

    def test_data_exists(self, mdb_test_collection):
        assert mdb_test_collection.find().count() == 1
