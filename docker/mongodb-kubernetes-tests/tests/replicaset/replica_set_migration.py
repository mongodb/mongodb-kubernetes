import pymongo
import pytest
from kubetester import try_load
from kubetester.kubetester import assert_statefulset_architecture, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import get_default_architecture
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester
from kubetester.phase import Phase
from pytest import fixture

MDB_RESOURCE_NAME = "replica-set-migration"


@fixture(scope="module")
def mdb(namespace, custom_mdb_version: str):
    resource = MongoDB.from_yaml(
        load_fixture("replica-set.yaml"),
        namespace=namespace,
        name=MDB_RESOURCE_NAME,
    )

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    return resource


@fixture(scope="module")
def mongo_tester(mdb: MongoDB):
    return mdb.tester()


@fixture(scope="module")
def mdb_health_checker(mongo_tester: MongoTester) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(
        mongo_tester,
        allowed_sequential_failures=1,
        health_function_params={
            "attempts": 1,
            "write_concern": pymongo.WriteConcern(w="majority"),
        },
    )


@pytest.mark.e2e_replica_set_migration
class TestReplicaSetMigrationStatic:

    def test_create_cluster(self, mdb: MongoDB):
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running)

    def test_start_health_checker(self, mdb_health_checker):
        mdb_health_checker.start()

    def test_migrate_architecture(self, mdb: MongoDB):
        """
        If the E2E is running with default architecture as non-static,
        then the test will migrate to static and vice versa.
        """
        original_default_architecture = get_default_architecture()
        target_architecture = "non-static" if original_default_architecture == "static" else "static"

        mdb.trigger_architecture_migration()

        mdb.load()
        assert mdb["metadata"]["annotations"]["mongodb.com/v1.architecture"] == target_architecture

        mdb.assert_abandons_phase(Phase.Running, timeout=1000)
        mdb.assert_reaches_phase(Phase.Running, timeout=1000)

        # Read StatefulSet after successful reconciliation
        sts = mdb.read_statefulset()
        assert_statefulset_architecture(sts, target_architecture)

    def test_mdb_healthy_throughout_change_version(self, mdb_health_checker):
        mdb_health_checker.assert_healthiness()
