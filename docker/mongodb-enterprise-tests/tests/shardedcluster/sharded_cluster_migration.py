import pymongo
import pytest
from kubetester import MongoDB, try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.kubetester import is_multi_cluster, skip_if_multi_cluster
from kubetester.mongodb import Phase
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester
from kubetester.operator import Operator
from pytest import fixture
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_mongos_service_names,
)

MDB_RESOURCE_NAME = "sharded-cluster-migration"


@fixture(scope="module")
def mdb(namespace, custom_mdb_version: str):
    resource = MongoDB.from_yaml(
        load_fixture("sharded-cluster.yaml"),
        namespace=namespace,
        name=MDB_RESOURCE_NAME,
    )

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.set_architecture_annotation()

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    return resource


@fixture(scope="module")
def mongo_tester(mdb: MongoDB):
    service_names = get_mongos_service_names(mdb)
    return mdb.tester(use_ssl=False, service_names=service_names)


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


@pytest.mark.e2e_sharded_cluster_migration
class TestShardedClusterMigrationStatic:

    def test_install_operator(self, operator: Operator):
        operator.assert_is_running()

    def test_create_cluster(self, mdb: MongoDB):
        mdb.update()
        mdb.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_start_health_checker(self, mdb_health_checker):
        mdb_health_checker.start()

    def test_migrate_architecture(self, mdb: MongoDB):
        mdb.trigger_architecture_migration()
        mdb.assert_abandons_phase(Phase.Running, timeout=1200)
        mdb.assert_reaches_phase(Phase.Running, timeout=1200)

    @skip_if_multi_cluster()  # Currently we are experiencing more than single failure during migration. More info
    # in the ticket -> https://jira.mongodb.org/browse/CLOUDP-286686
    def test_mdb_healthy_throughout_change_version(self, mdb_health_checker):
        mdb_health_checker.assert_healthiness()
