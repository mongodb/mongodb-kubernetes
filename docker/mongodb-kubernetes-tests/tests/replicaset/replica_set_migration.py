import pymongo
import pytest
from kubetester import try_load
from kubetester.kubetester import assert_statefulset_architecture, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import get_migration_target_architecture, get_static_ga_migration_target, run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_proxy import query_proxy
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester
from kubetester.phase import Phase
from pytest import fixture

MDB_RESOURCE_NAME = "replica-set-migration"
STATIC_TO_STATIC_GA_RESOURCE_NAME = "replica-set-static-to-static-ga"


@fixture(scope="module")
def mdb(namespace, custom_mdb_version: str):
    resource = MongoDB.from_yaml(
        load_fixture("replica-set.yaml"),
        namespace=namespace,
        name=MDB_RESOURCE_NAME,
    )

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    try_load(resource)
    return resource


@fixture(scope="module")
def mongo_tester(mdb: MongoDB):
    return mdb.tester()


@fixture(scope="module")
def mdb_health_checker(mongo_tester: MongoTester) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(mongo_tester)


@pytest.mark.e2e_replica_set_migration
class TestReplicaSetMigrationStatic:

    def test_create_cluster(self, mdb: MongoDB):
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running)

    def test_start_health_checker(self, mdb_health_checker):
        mdb_health_checker.start()

    def test_migrate_architecture(self, mdb: MongoDB):
        target_architecture = get_migration_target_architecture()

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


@fixture(scope="module")
def static_to_static_ga_mdb(namespace, custom_mdb_version: str, mdb: MongoDB) -> MongoDB:
    """A replica set that starts on legacy static and moves to static-ga.

    The variant default never selects this pair: get_migration_target_architecture() sends legacy static to
    non-static. The starting annotation is therefore pinned, and the target is named explicitly.

    The operator allows one MongoDB cluster per Ops Manager project, and this namespace maps to one project.
    This resource therefore replaces the one the previous class migrated, which is deleted first.
    """
    if try_load(mdb):
        mdb.delete()
        run_periodically(lambda: not try_load(mdb), timeout=600, msg="the previous replica set to be deleted")

    resource = MongoDB.from_yaml(
        load_fixture("replica-set.yaml"),
        namespace=namespace,
        name=STATIC_TO_STATIC_GA_RESOURCE_NAME,
    )

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"

    try_load(resource)
    return resource


@fixture(scope="module")
def static_to_static_ga_mongo_tester(static_to_static_ga_mdb: MongoDB) -> MongoTester:
    return static_to_static_ga_mdb.tester()


@fixture(scope="module")
def static_to_static_ga_health_checker(
    static_to_static_ga_mongo_tester: MongoTester,
) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(static_to_static_ga_mongo_tester)


@pytest.mark.e2e_replica_set_migration
class TestReplicaSetMigrationStaticToStaticGA:
    """Legacy static to static-ga, the pair the variant default never selects."""

    def test_create_cluster(self, static_to_static_ga_mdb: MongoDB):
        static_to_static_ga_mdb.update()
        static_to_static_ga_mdb.assert_reaches_phase(Phase.Running)

    def test_cluster_starts_on_legacy_static(self, static_to_static_ga_mdb: MongoDB):
        assert_statefulset_architecture(static_to_static_ga_mdb.read_statefulset(), "static")

    def test_start_health_checker(self, static_to_static_ga_health_checker: MongoDBBackgroundTester):
        static_to_static_ga_health_checker.start()

    def test_migrate_architecture(self, static_to_static_ga_mdb: MongoDB):
        target_architecture = get_static_ga_migration_target()

        static_to_static_ga_mdb.trigger_architecture_migration(target_architecture)

        static_to_static_ga_mdb.load()
        assert static_to_static_ga_mdb["metadata"]["annotations"]["mongodb.com/v1.architecture"] == target_architecture

        static_to_static_ga_mdb.assert_abandons_phase(Phase.Running, timeout=1000)
        static_to_static_ga_mdb.assert_reaches_phase(Phase.Running, timeout=1000)

        assert_statefulset_architecture(static_to_static_ga_mdb.read_statefulset(), target_architecture)

    def test_proxy_serves_the_process(self, static_to_static_ga_mdb: MongoDB, namespace: str):
        for member in range(static_to_static_ga_mdb.get_members()):
            status = query_proxy(f"{static_to_static_ga_mdb.name}-{member}", namespace)
            assert status["state"] == "running", status
            assert status["role"] == "main", status

    def test_mdb_healthy_throughout_change_version(self, static_to_static_ga_health_checker: MongoDBBackgroundTester):
        static_to_static_ga_health_checker.assert_healthiness()
