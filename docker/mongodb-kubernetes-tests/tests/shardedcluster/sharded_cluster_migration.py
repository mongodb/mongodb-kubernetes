import pytest
from kubetester import try_load
from kubetester.kubetester import assert_statefulset_architecture, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import get_static_ga_migration_target, is_multi_cluster
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

MDB_RESOURCE_NAME = "sharded-cluster-migration"


@fixture(scope="module")
def mdb(namespace, custom_mdb_version: str):
    """A sharded cluster that starts on legacy static and walks the full architecture chain.

    The start is pinned to legacy static: the variant default never selects it, and
    get_migration_target_architecture() sends legacy static straight to non-static, skipping static-ga.
    """
    resource = MongoDB.from_yaml(
        load_fixture("sharded-cluster.yaml"),
        namespace=namespace,
        name=MDB_RESOURCE_NAME,
    )

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    try_load(resource)
    return resource


@fixture(scope="module")
def mongo_tester(mdb: MongoDB):
    service_names = get_mongos_service_names(mdb)
    return mdb.tester(use_ssl=False, service_names=service_names)


@fixture(scope="module")
def mdb_health_checker(mongo_tester: MongoTester) -> MongoDBBackgroundTester:
    # Architecture migration (static ↔ non-static) requires restarting many components, which can cause
    # extended unavailability. This shouldn't happen after fixes in CLOUDP-286686 and CLOUDP-375105, but it happens
    # again please refer to those tickets for understanding of the issue.
    return MongoDBBackgroundTester(mongo_tester)


@pytest.mark.e2e_sharded_cluster_migration
class TestShardedClusterMigration:
    """Legacy static to static-ga to non-static, keeping the data written before the first hop."""

    def test_install_operator(self, operator: Operator):
        operator.wait_for_operator_ready()

    def test_create_cluster(self, mdb: MongoDB):
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_cluster_starts_on_legacy_static(self, mdb: MongoDB):
        for statefulset in _statefulsets(mdb):
            assert_statefulset_architecture(statefulset, "static")

    def test_write_migration_data(self, mongo_tester: MongoTester):
        insert_migration_data(mongo_tester)

    def test_start_health_checker(self, mdb_health_checker: MongoDBBackgroundTester):
        mdb_health_checker.start()

    def test_migrate_to_static_ga(self, mdb: MongoDB):
        # Named rather than derived: get_migration_target_architecture() sends legacy static to non-static.
        self._migrate(mdb, get_static_ga_migration_target())

    def test_proxy_serves_the_mongod_processes(self, mdb: MongoDB, namespace: str):
        for pod in get_all_shards_pod_names(mdb) + get_config_server_pod_names(mdb):
            status = query_proxy(pod, namespace)
            assert status["state"] == "running", (pod, status)

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

        mdb.assert_abandons_phase(Phase.Running, timeout=200)
        mdb.assert_reaches_phase(Phase.Running, timeout=1600)

        for statefulset in _statefulsets(mdb):
            assert_statefulset_architecture(statefulset, target_architecture)


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
