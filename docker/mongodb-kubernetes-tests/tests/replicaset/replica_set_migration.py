import pytest
from kubetester import try_load
from kubetester.kubetester import assert_statefulset_architecture, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import get_static_ga_migration_target
from kubetester.mongodb import MongoDB
from kubetester.mongodb_proxy import query_proxy
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester
from kubetester.phase import Phase
from pytest import fixture
from tests.vm_migration.vm_migration_common_helper import assert_migration_data_exists, insert_migration_data

MDB_RESOURCE_NAME = "replica-set-migration"


@fixture(scope="module")
def mdb(namespace, custom_mdb_version: str):
    """A replica set that starts on legacy static and walks the full architecture chain.

    The start is pinned to legacy static: the variant default never selects it, and
    get_migration_target_architecture() sends legacy static straight to non-static, skipping static-ga.
    """
    resource = MongoDB.from_yaml(
        load_fixture("replica-set.yaml"),
        namespace=namespace,
        name=MDB_RESOURCE_NAME,
    )

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"

    try_load(resource)
    return resource


@fixture(scope="module")
def mongo_tester(mdb: MongoDB):
    return mdb.tester()


@fixture(scope="module")
def mdb_health_checker(mongo_tester: MongoTester) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(mongo_tester)


@pytest.mark.e2e_replica_set_migration
class TestReplicaSetMigration:
    """Legacy static to static-ga to non-static, keeping the data written before the first hop."""

    def test_create_cluster(self, mdb: MongoDB):
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running)

    def test_cluster_starts_on_legacy_static(self, mdb: MongoDB):
        assert_statefulset_architecture(mdb.read_statefulset(), "static")

    def test_write_migration_data(self, mongo_tester: MongoTester):
        insert_migration_data(mongo_tester)

    def test_start_health_checker(self, mdb_health_checker: MongoDBBackgroundTester):
        mdb_health_checker.start()

    def test_migrate_to_static_ga(self, mdb: MongoDB):
        # Named rather than derived: get_migration_target_architecture() sends legacy static to non-static.
        self._migrate(mdb, get_static_ga_migration_target())

    def test_proxy_serves_the_process(self, mdb: MongoDB, namespace: str):
        for member in range(mdb.get_members()):
            status = query_proxy(f"{mdb.name}-{member}", namespace)
            assert status["state"] == "running", status
            assert status["role"] == "main", status

    def test_migration_data_survived_static_ga(self, mongo_tester: MongoTester):
        assert_migration_data_exists(mongo_tester)

    def test_migrate_to_non_static(self, mdb: MongoDB):
        self._migrate(mdb, "non-static")

    def test_migration_data_survived_non_static(self, mongo_tester: MongoTester):
        assert_migration_data_exists(mongo_tester)

    def test_mdb_healthy_throughout_change_version(self, mdb_health_checker: MongoDBBackgroundTester):
        mdb_health_checker.assert_healthiness()

    @staticmethod
    def _migrate(mdb: MongoDB, target_architecture: str):
        mdb.trigger_architecture_migration(target_architecture)

        mdb.load()
        assert mdb["metadata"]["annotations"]["mongodb.com/v1.architecture"] == target_architecture

        mdb.assert_abandons_phase(Phase.Running, timeout=1000)
        mdb.assert_reaches_phase(Phase.Running, timeout=1000)

        assert_statefulset_architecture(mdb.read_statefulset(), target_architecture)
