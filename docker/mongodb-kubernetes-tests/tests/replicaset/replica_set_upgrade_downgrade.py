import pymongo
from kubetester import try_load
from kubetester.kubetester import KubernetesTester, fcv_from_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester, ReplicaSetTester
from kubetester.phase import Phase
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
        allowed_sequential_failures=2,
        health_function_params={
            "attempts": 1,
            "write_concern": pymongo.WriteConcern(w="majority"),
        },
    )


@fixture
def mdb_test_collection(mongod_tester):
    collection = mongod_tester.client[TEST_DB]
    return collection[TEST_COLLECTION]


@fixture(scope="module")
def replica_set(namespace: str, custom_mdb_prev_version: str, cluster_domain: str) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-downgrade.yaml"), namespace=namespace)
    resource.set_version(custom_mdb_prev_version)
    if try_load(resource):
        return resource
    return resource.update()


@mark.e2e_replica_set_upgrade_downgrade
class TestReplicaSetUpgradeDowngradeCreate(KubernetesTester):

    def test_mdb_created(self, replica_set: MongoDB):
        replica_set.assert_reaches_phase(Phase.Running, timeout=1000)

    def test_start_mongod_background_tester(self, mdb_health_checker):
        mdb_health_checker.start()

    def test_db_connectable(self, mongod_tester, custom_mdb_prev_version: str):
        mongod_tester.assert_version(custom_mdb_prev_version)

    def test_insert_test_data(self, mdb_test_collection):
        mdb_test_collection.insert_one(TEST_DATA)


@mark.e2e_replica_set_upgrade_downgrade
class TestReplicaSetUpgradeDowngradeUpdate(KubernetesTester):

    def test_mongodb_upgrade(self, replica_set: MongoDB, custom_mdb_version: str, custom_mdb_prev_version: str):
        replica_set.load()
        replica_set.set_version(custom_mdb_version)
        fcv = fcv_from_version(custom_mdb_prev_version)
        replica_set["spec"]["featureCompatibilityVersion"] = fcv
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, timeout=700)
        replica_set.tester().assert_version(custom_mdb_version)

    def test_mongodb_version_fcv(self, replica_set: MongoDB, custom_mdb_prev_version: str):
        # no fcv is set; that means we will use the smaller one between custom_mdb_version and custom_mdb_prev_version
        # 5 -> 6, fcv: 5-> 5
        major_minor_prev = custom_mdb_prev_version.split(".")

        assert replica_set.get_status_fcv() == f"{major_minor_prev[0]}.{major_minor_prev[1]}"

    def test_db_connectable(self, mongod_tester, custom_mdb_version: str):
        mongod_tester.assert_version(custom_mdb_version)


@mark.e2e_replica_set_upgrade_downgrade
class TestReplicaSetUpgradeDowngradeRevert(KubernetesTester):

    def test_mongodb_downgrade(self, replica_set: MongoDB, custom_mdb_prev_version: str, custom_mdb_version: str):
        replica_set.load()
        replica_set.set_version(custom_mdb_prev_version)
        replica_set.update()

        replica_set.assert_reaches_phase(Phase.Running, timeout=1000)
        replica_set.tester().assert_version(custom_mdb_prev_version)

    def test_mongodb_version_fcv(self, replica_set: MongoDB, custom_mdb_prev_version: str):
        # no fcv is set; that means we will use the smaller one between custom_mdb_version and custom_mdb_prev_version
        # 6 -> 5, fcv: 5-> 5
        major_minor = custom_mdb_prev_version.split(".")

        assert replica_set.get_status_fcv() == f"{major_minor[0]}.{major_minor[1]}"

    def test_db_connectable(self, mongod_tester, custom_mdb_prev_version: str):
        mongod_tester.assert_version(custom_mdb_prev_version)

    def test_mdb_healthy_throughout_change_version(self, mdb_health_checker):
        mdb_health_checker.assert_healthiness()

    def test_data_exists(self, mdb_test_collection):
        assert mdb_test_collection.estimated_document_count() == 1
