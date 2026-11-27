import pytest
from kubetester import try_load
from kubetester.kubetester import assert_statefulset_architecture, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import get_default_architecture, get_static_ga_migration_target, run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_proxy import query_proxy
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester
from kubetester.phase import Phase
from pytest import fixture
from tests.vm_migration.vm_migration_common_helper import assert_migration_data_exists, insert_migration_data

NON_STATIC_TO_STATIC_GA = "replica-set-migration-non-static-to-static-ga"
NON_STATIC_TO_STATIC = "replica-set-migration-non-static-to-static"
STATIC_TO_STATIC_GA = "replica-set-migration-static-to-static-ga"

_CLUSTER_NAMES = (NON_STATIC_TO_STATIC_GA, NON_STATIC_TO_STATIC, STATIC_TO_STATIC_GA)

# static-ga is a target in every supported transition, never a source, so no single resource can walk all three.
skip_unless_non_static = pytest.mark.skipif(
    get_default_architecture() != "non-static",
    reason="Runs only where the variant default architecture is non-static",
)
skip_unless_static = pytest.mark.skipif(
    get_default_architecture() != "static",
    reason="Runs only where the variant default architecture is static",
)


def _new_cluster(namespace: str, name: str, version: str, architecture: str) -> MongoDB:
    """Create this class's cluster after removing whatever the previous class left behind.

    The operator allows one MongoDB cluster per Ops Manager project, and this namespace maps to one project,
    so the classes cannot run side by side.
    """
    for existing_name in _CLUSTER_NAMES:
        existing = MongoDB.from_yaml(load_fixture("replica-set.yaml"), namespace=namespace, name=existing_name)
        if try_load(existing):
            existing.delete()
            run_periodically(lambda: not try_load(existing), timeout=600, msg=f"{existing_name} to be deleted")

    resource = MongoDB.from_yaml(load_fixture("replica-set.yaml"), namespace=namespace, name=name)
    resource.set_version(ensure_ent_version(version))
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = architecture

    try_load(resource)
    return resource


def _migrate(mdb: MongoDB, target_architecture: str) -> None:
    mdb.trigger_architecture_migration(target_architecture)

    mdb.load()
    assert mdb["metadata"]["annotations"]["mongodb.com/v1.architecture"] == target_architecture

    mdb.assert_abandons_phase(Phase.Running, timeout=1000)
    mdb.assert_reaches_phase(Phase.Running, timeout=1000)

    assert_statefulset_architecture(mdb.read_statefulset(), target_architecture)


@fixture(scope="module")
def non_static_to_static_ga_mdb(namespace, custom_mdb_version: str) -> MongoDB:
    return _new_cluster(namespace, NON_STATIC_TO_STATIC_GA, custom_mdb_version, "non-static")


@fixture(scope="module")
def non_static_to_static_mdb(namespace, custom_mdb_version: str) -> MongoDB:
    return _new_cluster(namespace, NON_STATIC_TO_STATIC, custom_mdb_version, "non-static")


@fixture(scope="module")
def static_to_static_ga_mdb(namespace, custom_mdb_version: str) -> MongoDB:
    return _new_cluster(namespace, STATIC_TO_STATIC_GA, custom_mdb_version, "static")


@fixture(scope="module")
def non_static_to_static_ga_health_checker(non_static_to_static_ga_mdb: MongoDB) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(non_static_to_static_ga_mdb.tester())


@fixture(scope="module")
def static_to_static_ga_health_checker(static_to_static_ga_mdb: MongoDB) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(static_to_static_ga_mdb.tester())


@skip_unless_non_static
@pytest.mark.e2e_replica_set_migration
class TestReplicaSetMigrationNonStaticToStaticGA:
    """non-static to static-ga, the pair the non-static variant default selects."""

    def test_create_cluster(self, non_static_to_static_ga_mdb: MongoDB):
        non_static_to_static_ga_mdb.update()
        non_static_to_static_ga_mdb.assert_reaches_phase(Phase.Running)

    def test_cluster_starts_on_non_static(self, non_static_to_static_ga_mdb: MongoDB):
        assert_statefulset_architecture(non_static_to_static_ga_mdb.read_statefulset(), "non-static")

    def test_write_migration_data(self, non_static_to_static_ga_mdb: MongoDB):
        insert_migration_data(non_static_to_static_ga_mdb.tester())

    def test_start_health_checker(self, non_static_to_static_ga_health_checker: MongoDBBackgroundTester):
        non_static_to_static_ga_health_checker.start()

    def test_migrate_to_static_ga(self, non_static_to_static_ga_mdb: MongoDB):
        _migrate(non_static_to_static_ga_mdb, get_static_ga_migration_target())

    def test_proxy_serves_the_process(self, non_static_to_static_ga_mdb: MongoDB, namespace: str):
        for member in range(non_static_to_static_ga_mdb.get_members()):
            status = query_proxy(f"{non_static_to_static_ga_mdb.name}-{member}", namespace)
            assert status["state"] == "running", status
            assert status["role"] == "main", status

    def test_migration_data_survived(self, non_static_to_static_ga_mdb: MongoDB):
        assert_migration_data_exists(non_static_to_static_ga_mdb.tester())

    def test_mdb_healthy_throughout_change_version(
        self, non_static_to_static_ga_health_checker: MongoDBBackgroundTester
    ):
        non_static_to_static_ga_health_checker.assert_healthiness()
        non_static_to_static_ga_health_checker.stop()


@skip_unless_non_static
@pytest.mark.e2e_replica_set_migration
class TestReplicaSetMigrationNonStaticToStatic:
    """non-static to static, the pair the non-static variant default never selects."""

    def test_create_cluster(self, non_static_to_static_mdb: MongoDB):
        non_static_to_static_mdb.update()
        non_static_to_static_mdb.assert_reaches_phase(Phase.Running)

    def test_cluster_starts_on_non_static(self, non_static_to_static_mdb: MongoDB):
        assert_statefulset_architecture(non_static_to_static_mdb.read_statefulset(), "non-static")

    def test_write_migration_data(self, non_static_to_static_mdb: MongoDB):
        insert_migration_data(non_static_to_static_mdb.tester())

    def test_migrate_to_static(self, non_static_to_static_mdb: MongoDB):
        _migrate(non_static_to_static_mdb, "static")

    def test_migration_data_survived(self, non_static_to_static_mdb: MongoDB):
        assert_migration_data_exists(non_static_to_static_mdb.tester())


@skip_unless_static
@pytest.mark.e2e_replica_set_migration
class TestReplicaSetMigrationStaticToStaticGA:
    """Legacy static to static-ga, the pair the static variant default never selects."""

    def test_create_cluster(self, static_to_static_ga_mdb: MongoDB):
        static_to_static_ga_mdb.update()
        static_to_static_ga_mdb.assert_reaches_phase(Phase.Running)

    def test_cluster_starts_on_legacy_static(self, static_to_static_ga_mdb: MongoDB):
        assert_statefulset_architecture(static_to_static_ga_mdb.read_statefulset(), "static")

    def test_write_migration_data(self, static_to_static_ga_mdb: MongoDB):
        insert_migration_data(static_to_static_ga_mdb.tester())

    def test_start_health_checker(self, static_to_static_ga_health_checker: MongoDBBackgroundTester):
        static_to_static_ga_health_checker.start()

    def test_migrate_to_static_ga(self, static_to_static_ga_mdb: MongoDB):
        _migrate(static_to_static_ga_mdb, get_static_ga_migration_target())

    def test_proxy_serves_the_process(self, static_to_static_ga_mdb: MongoDB, namespace: str):
        for member in range(static_to_static_ga_mdb.get_members()):
            status = query_proxy(f"{static_to_static_ga_mdb.name}-{member}", namespace)
            assert status["state"] == "running", status
            assert status["role"] == "main", status

    def test_migration_data_survived(self, static_to_static_ga_mdb: MongoDB):
        assert_migration_data_exists(static_to_static_ga_mdb.tester())

    def test_mdb_healthy_throughout_change_version(self, static_to_static_ga_health_checker: MongoDBBackgroundTester):
        static_to_static_ga_health_checker.assert_healthiness()
        static_to_static_ga_health_checker.stop()
