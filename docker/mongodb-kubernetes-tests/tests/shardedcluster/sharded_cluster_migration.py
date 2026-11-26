import pymongo
import pytest
from kubetester import try_load
from kubetester.kubetester import assert_statefulset_architecture, ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import get_migration_target_architecture, get_static_ga_migration_target, is_multi_cluster
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

MDB_RESOURCE_NAME = "sharded-cluster-migration"
STATIC_TO_STATIC_GA_RESOURCE_NAME = "sharded-cluster-static-to-static-ga"


@fixture(scope="module")
def mdb(namespace, custom_mdb_version: str):
    resource = MongoDB.from_yaml(
        load_fixture("sharded-cluster.yaml"),
        namespace=namespace,
        name=MDB_RESOURCE_NAME,
    )

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

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
class TestShardedClusterMigrationStatic:

    def test_install_operator(self, operator: Operator):
        operator.wait_for_operator_ready()

    def test_create_cluster(self, mdb: MongoDB):
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_start_health_checker(self, mdb_health_checker):
        mdb_health_checker.start()

    def test_migrate_architecture(self, mdb: MongoDB):
        target_architecture = get_migration_target_architecture()

        mdb.trigger_architecture_migration()

        mdb.load()
        assert mdb["metadata"]["annotations"]["mongodb.com/v1.architecture"] == target_architecture

        mdb.assert_abandons_phase(Phase.Running, timeout=200)
        mdb.assert_reaches_phase(Phase.Running, timeout=1600)

        # Read StatefulSet after successful reconciliation
        for statefulset in _statefulsets(mdb):
            assert_statefulset_architecture(statefulset, target_architecture)

    def test_mdb_healthy_throughout_change_version(self, mdb_health_checker):
        mdb_health_checker.assert_healthiness()


@fixture(scope="module")
def static_to_static_ga_mdb(namespace, custom_mdb_version: str) -> MongoDB:
    """A second sharded cluster that starts on legacy static and moves to static-ga.

    The variant default never selects this pair: get_migration_target_architecture() sends legacy static to
    non-static. The starting annotation is therefore pinned, and the target is named explicitly.
    """
    resource = MongoDB.from_yaml(
        load_fixture("sharded-cluster.yaml"),
        namespace=namespace,
        name=STATIC_TO_STATIC_GA_RESOURCE_NAME,
    )

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    try_load(resource)
    return resource


@fixture(scope="module")
def static_to_static_ga_mongo_tester(static_to_static_ga_mdb: MongoDB) -> MongoTester:
    return static_to_static_ga_mdb.tester(
        use_ssl=False, service_names=get_mongos_service_names(static_to_static_ga_mdb)
    )


@fixture(scope="module")
def static_to_static_ga_health_checker(
    static_to_static_ga_mongo_tester: MongoTester,
) -> MongoDBBackgroundTester:
    # The migration restarts every component, which can cause extended unavailability. See CLOUDP-286686 and
    # CLOUDP-375105 for the earlier occurrences.
    return MongoDBBackgroundTester(static_to_static_ga_mongo_tester)


@pytest.mark.e2e_sharded_cluster_migration
class TestShardedClusterMigrationStaticToStaticGA:
    """Legacy static to static-ga, the pair the variant default never selects."""

    def test_create_cluster(self, static_to_static_ga_mdb: MongoDB):
        static_to_static_ga_mdb.update()
        static_to_static_ga_mdb.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_cluster_starts_on_legacy_static(self, static_to_static_ga_mdb: MongoDB):
        for statefulset in _statefulsets(static_to_static_ga_mdb):
            assert_statefulset_architecture(statefulset, "static")

    def test_start_health_checker(self, static_to_static_ga_health_checker: MongoDBBackgroundTester):
        static_to_static_ga_health_checker.start()

    def test_migrate_architecture(self, static_to_static_ga_mdb: MongoDB):
        target_architecture = get_static_ga_migration_target()

        static_to_static_ga_mdb.trigger_architecture_migration(target_architecture)

        static_to_static_ga_mdb.load()
        assert static_to_static_ga_mdb["metadata"]["annotations"]["mongodb.com/v1.architecture"] == target_architecture

        static_to_static_ga_mdb.assert_abandons_phase(Phase.Running, timeout=200)
        static_to_static_ga_mdb.assert_reaches_phase(Phase.Running, timeout=1600)

        for statefulset in _statefulsets(static_to_static_ga_mdb):
            assert_statefulset_architecture(statefulset, target_architecture)

    def test_proxy_serves_the_mongod_processes(self, static_to_static_ga_mdb: MongoDB, namespace: str):
        for pod in get_all_shards_pod_names(static_to_static_ga_mdb) + get_config_server_pod_names(
            static_to_static_ga_mdb
        ):
            status = query_proxy(pod, namespace)
            assert status["state"] == "running", (pod, status)

    def test_mdb_healthy_throughout_change_version(self, static_to_static_ga_health_checker: MongoDBBackgroundTester):
        static_to_static_ga_health_checker.assert_healthiness()


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
