import pytest
from kubetester import try_load
from kubetester.kubetester import assert_statefulset_architecture, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import (
    get_default_architecture,
    get_static_ga_migration_target,
    is_multi_cluster,
    run_periodically,
)
from kubetester.mongodb import MongoDB
from kubetester.mongodb_proxy import query_proxy
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_all_shards_pod_names,
    get_config_server_pod_names,
    get_member_cluster_clients_using_cluster_mapping,
    get_mongos_service_names,
)
from tests.vm_migration.vm_migration_common_helper import assert_migration_data_exists, insert_migration_data

# One name for every class: only one class runs per variant, and the previous class's cluster is deleted first.
MDB_RESOURCE_NAME = "sharded-cluster-migration"

# static-ga is a target in every supported transition, never a source, so no single resource can walk all three.
skip_unless_non_static = pytest.mark.skipif(
    get_default_architecture() != "non-static",
    reason="Runs only where the variant default architecture is non-static",
)
skip_unless_static = pytest.mark.skipif(
    get_default_architecture() != "static",
    reason="Runs only where the variant default architecture is static",
)


def _new_cluster(namespace: str, version: str, architecture: str) -> MongoDB:
    """Create this class's cluster after removing whatever the previous class left behind.

    The operator allows one MongoDB cluster per Ops Manager project, and this namespace maps to one project,
    so the classes cannot run side by side.
    """
    existing = MongoDB.from_yaml(load_fixture("sharded-cluster.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME)
    if try_load(existing):
        existing.delete()
        run_periodically(lambda: not try_load(existing), timeout=600, msg=f"{MDB_RESOURCE_NAME} to be deleted")

    resource = MongoDB.from_yaml(load_fixture("sharded-cluster.yaml"), namespace=namespace, name=MDB_RESOURCE_NAME)
    resource.set_version(ensure_ent_version(version))
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = architecture

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    try_load(resource)
    return resource


def _migrate(mdb: MongoDB, target_architecture: str) -> None:
    mdb.trigger_architecture_migration(target_architecture)

    mdb.load()
    assert mdb["metadata"]["annotations"]["mongodb.com/v1.architecture"] == target_architecture

    mdb.assert_abandons_phase(Phase.Running, timeout=200)
    mdb.assert_reaches_phase(Phase.Running, timeout=1600)

    for statefulset in _statefulsets(mdb):
        assert_statefulset_architecture(statefulset, target_architecture)


def _tester(mdb: MongoDB) -> MongoTester:
    return mdb.tester(use_ssl=False, service_names=get_mongos_service_names(mdb))


@fixture(scope="module")
def operator_ready(operator: Operator) -> Operator:
    """Wait for the operator before any class creates a cluster.

    This is a fixture rather than a test so it runs whichever class the variant selects.
    """
    operator.wait_for_operator_ready()
    return operator


@fixture(scope="module")
def non_static_to_static_ga_mdb(namespace, custom_mdb_version: str, operator_ready: Operator) -> MongoDB:
    return _new_cluster(namespace, custom_mdb_version, "non-static")


@fixture(scope="module")
def non_static_to_static_mdb(namespace, custom_mdb_version: str, operator_ready: Operator) -> MongoDB:
    return _new_cluster(namespace, custom_mdb_version, "non-static")


@fixture(scope="module")
def static_to_static_ga_mdb(namespace, custom_mdb_version: str, operator_ready: Operator) -> MongoDB:
    return _new_cluster(namespace, custom_mdb_version, "static")


@fixture(scope="module")
def non_static_to_static_ga_health_checker(non_static_to_static_ga_mdb: MongoDB) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(_tester(non_static_to_static_ga_mdb))


@fixture(scope="module")
def static_to_static_ga_health_checker(static_to_static_ga_mdb: MongoDB) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(_tester(static_to_static_ga_mdb))


@skip_unless_non_static
@pytest.mark.e2e_sharded_cluster_migration
class TestShardedClusterMigrationNonStaticToStaticGA:
    """non-static to static-ga, the pair the non-static variant default selects."""

    def test_create_cluster(self, non_static_to_static_ga_mdb: MongoDB):
        non_static_to_static_ga_mdb.update()
        non_static_to_static_ga_mdb.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_cluster_starts_on_non_static(self, non_static_to_static_ga_mdb: MongoDB):
        for statefulset in _statefulsets(non_static_to_static_ga_mdb):
            assert_statefulset_architecture(statefulset, "non-static")

    def test_write_migration_data(self, non_static_to_static_ga_mdb: MongoDB):
        insert_migration_data(_tester(non_static_to_static_ga_mdb))

    def test_start_health_checker(self, non_static_to_static_ga_health_checker: MongoDBBackgroundTester):
        non_static_to_static_ga_health_checker.start()

    def test_migrate_to_static_ga(self, non_static_to_static_ga_mdb: MongoDB):
        _migrate(non_static_to_static_ga_mdb, get_static_ga_migration_target())

    def test_proxy_serves_the_mongod_processes(self, non_static_to_static_ga_mdb: MongoDB, namespace: str):
        for pod in get_all_shards_pod_names(non_static_to_static_ga_mdb) + get_config_server_pod_names(
            non_static_to_static_ga_mdb
        ):
            status = query_proxy(pod, namespace)
            assert status["state"] == "running", (pod, status)

    def test_migration_data_survived(self, non_static_to_static_ga_mdb: MongoDB):
        assert_migration_data_exists(_tester(non_static_to_static_ga_mdb))

    def test_mdb_healthy_throughout_change_version(
        self, non_static_to_static_ga_health_checker: MongoDBBackgroundTester
    ):
        non_static_to_static_ga_health_checker.assert_healthiness()
        non_static_to_static_ga_health_checker.stop()


@skip_unless_non_static
@pytest.mark.e2e_sharded_cluster_migration
class TestShardedClusterMigrationNonStaticToStatic:
    """non-static to static, the pair the non-static variant default never selects."""

    def test_create_cluster(self, non_static_to_static_mdb: MongoDB):
        non_static_to_static_mdb.update()
        non_static_to_static_mdb.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_cluster_starts_on_non_static(self, non_static_to_static_mdb: MongoDB):
        for statefulset in _statefulsets(non_static_to_static_mdb):
            assert_statefulset_architecture(statefulset, "non-static")

    def test_write_migration_data(self, non_static_to_static_mdb: MongoDB):
        insert_migration_data(_tester(non_static_to_static_mdb))

    def test_migrate_to_static(self, non_static_to_static_mdb: MongoDB):
        _migrate(non_static_to_static_mdb, "static")

    def test_migration_data_survived(self, non_static_to_static_mdb: MongoDB):
        assert_migration_data_exists(_tester(non_static_to_static_mdb))


@skip_unless_static
@pytest.mark.e2e_sharded_cluster_migration
class TestShardedClusterMigrationStaticToStaticGA:
    """Legacy static to static-ga, the pair the static variant default never selects."""

    def test_create_cluster(self, static_to_static_ga_mdb: MongoDB):
        static_to_static_ga_mdb.update()
        static_to_static_ga_mdb.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_cluster_starts_on_legacy_static(self, static_to_static_ga_mdb: MongoDB):
        for statefulset in _statefulsets(static_to_static_ga_mdb):
            assert_statefulset_architecture(statefulset, "static")

    def test_write_migration_data(self, static_to_static_ga_mdb: MongoDB):
        insert_migration_data(_tester(static_to_static_ga_mdb))

    def test_start_health_checker(self, static_to_static_ga_health_checker: MongoDBBackgroundTester):
        static_to_static_ga_health_checker.start()

    def test_migrate_to_static_ga(self, static_to_static_ga_mdb: MongoDB):
        _migrate(static_to_static_ga_mdb, get_static_ga_migration_target())

    def test_proxy_serves_the_mongod_processes(self, static_to_static_ga_mdb: MongoDB, namespace: str):
        for pod in get_all_shards_pod_names(static_to_static_ga_mdb) + get_config_server_pod_names(
            static_to_static_ga_mdb
        ):
            status = query_proxy(pod, namespace)
            assert status["state"] == "running", (pod, status)

    def test_migration_data_survived(self, static_to_static_ga_mdb: MongoDB):
        assert_migration_data_exists(_tester(static_to_static_ga_mdb))

    def test_mdb_healthy_throughout_change_version(self, static_to_static_ga_health_checker: MongoDBBackgroundTester):
        static_to_static_ga_health_checker.assert_healthiness()
        static_to_static_ga_health_checker.stop()


def _statefulsets(mdb: MongoDB) -> list:
    """Every StatefulSet of the cluster: one per shard, the config servers and mongos."""
    statefulsets = []
    for cluster_member_client in get_member_cluster_clients_using_cluster_mapping(mdb.name, mdb.namespace):
        cluster_idx = cluster_member_client.cluster_index
        for shard_idx in range(mdb["spec"]["shardCount"]):
            statefulsets.append(
                cluster_member_client.read_namespaced_stateful_set(
                    mdb.shard_statefulset_name(shard_idx, cluster_idx), mdb.namespace
                )
            )
        statefulsets.append(
            cluster_member_client.read_namespaced_stateful_set(
                mdb.config_srv_statefulset_name(cluster_idx), mdb.namespace
            )
        )
        statefulsets.append(
            cluster_member_client.read_namespaced_stateful_set(mdb.mongos_statefulset_name(cluster_idx), mdb.namespace)
        )
    return statefulsets
