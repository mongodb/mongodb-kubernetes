import pymongo
from kubetester import try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version, fcv_from_version
from kubetester.kubetester import fixture as load_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoDBBackgroundTester, MongoTester
from kubetester.operator import Operator
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.shardedcluster.conftest import (
    enable_multi_cluster_deployment,
    get_member_cluster_clients_using_cluster_mapping,
    get_mongos_service_names,
)


@fixture(scope="module")
def sc(namespace: str, custom_mdb_prev_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(load_fixture("sharded-cluster-downgrade.yaml"), namespace=namespace)

    if try_load(resource):
        return resource

    resource.set_version(ensure_ent_version(custom_mdb_prev_version))
    resource.set_architecture_annotation()

    if is_multi_cluster():
        enable_multi_cluster_deployment(
            resource=resource,
            shard_members_array=[1, 1, 1],
            mongos_members_array=[1, 1, 1],
            configsrv_members_array=[1, 1, 1],
        )

    return resource.update()


@fixture(scope="module")
def mongod_tester(sc: MongoDB) -> MongoTester:
    service_names = get_mongos_service_names(sc)

    return sc.tester(service_names=service_names)


@fixture(scope="module")
def mdb_health_checker(mongod_tester: MongoTester) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(
        mongod_tester,
        # After running multiple tests, it seems that on sharded_cluster version changes we have more sequential errors.
        allowed_sequential_failures=5,
        health_function_params={
            "attempts": 1,
            "write_concern": pymongo.WriteConcern(w="majority"),
            "tolerate_election_errors": True,
        },
    )


@mark.e2e_sharded_cluster_upgrade_downgrade
def test_install_operator(operator: Operator):
    operator.assert_is_running()


@mark.e2e_sharded_cluster_upgrade_downgrade
class TestShardedClusterUpgradeDowngradeCreate(KubernetesTester):

    def test_mdb_created(self, sc: MongoDB):
        sc.assert_reaches_phase(Phase.Running, timeout=1000)

    def test_start_mongod_background_tester(self, mdb_health_checker):
        mdb_health_checker.start()

    def test_db_connectable(self, mongod_tester: MongoTester, custom_mdb_prev_version: str):
        mongod_tester.assert_connectivity()
        mongod_tester.assert_version(custom_mdb_prev_version)


@mark.e2e_sharded_cluster_upgrade_downgrade
class TestShardedClusterUpgradeDowngradeUpdate:

    def test_mongodb_upgrade(self, sc: MongoDB, custom_mdb_version: str, custom_mdb_prev_version: str):
        sc.set_version(custom_mdb_version)
        fcv = fcv_from_version(custom_mdb_prev_version)
        sc["spec"]["featureCompatibilityVersion"] = fcv
        sc.update()
        sc.assert_reaches_phase(Phase.Running, timeout=2400)

    def test_db_connectable(self, mongod_tester: MongoTester, custom_mdb_version: str):
        mongod_tester.assert_connectivity()
        mongod_tester.assert_version(custom_mdb_version)


@mark.e2e_sharded_cluster_upgrade_downgrade
class TestShardedClusterUpgradeDowngradeRevert:

    def test_mongodb_downgrade(self, sc: MongoDB, custom_mdb_prev_version: str):
        sc.set_version(custom_mdb_prev_version)
        sc.update()
        sc.assert_reaches_phase(Phase.Running, timeout=2400)

    def test_db_connectable(self, mongod_tester: MongoTester, custom_mdb_prev_version):
        mongod_tester.assert_connectivity()
        mongod_tester.assert_version(custom_mdb_prev_version)

    def test_mdb_healthy_throughout_change_version(self, mdb_health_checker):
        mdb_health_checker.assert_healthiness()
