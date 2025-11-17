from kubetester.kubetester import ensure_ent_version, fcv_from_version
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongotester import MongoDBBackgroundTester
from kubetester.operator import Operator
from kubetester.phase import Phase


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


def test_create_mongodb_multi_running(mongodb_multi: MongoDBMulti | MongoDB, custom_mdb_prev_version: str):
    mongodb_multi.update()
    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)
    mongodb_multi.tester().assert_version(ensure_ent_version(custom_mdb_prev_version))


def test_start_background_checker(mdb_health_checker: MongoDBBackgroundTester):
    mdb_health_checker.start()


def test_mongodb_multi_upgrade(
    mongodb_multi: MongoDBMulti | MongoDB, custom_mdb_prev_version: str, custom_mdb_version: str
):
    mongodb_multi.load()
    mongodb_multi["spec"]["version"] = ensure_ent_version(custom_mdb_version)
    mongodb_multi["spec"]["featureCompatibilityVersion"] = fcv_from_version(custom_mdb_prev_version)
    mongodb_multi.update()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)

    mongodb_multi.tester().assert_version(ensure_ent_version(custom_mdb_version))


def test_upgraded_replica_set_is_reachable(mongodb_multi: MongoDBMulti | MongoDB):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


def test_mongodb_multi_downgrade(mongodb_multi: MongoDBMulti | MongoDB, custom_mdb_prev_version: str):
    mongodb_multi.load()
    mongodb_multi["spec"]["version"] = ensure_ent_version(custom_mdb_prev_version)
    mongodb_multi["spec"]["featureCompatibilityVersion"] = fcv_from_version(custom_mdb_prev_version)
    mongodb_multi.update()

    mongodb_multi.assert_reaches_phase(Phase.Running, timeout=700)
    mongodb_multi.tester().assert_version(ensure_ent_version(custom_mdb_prev_version))


def test_downgraded_replica_set_is_reachable(mongodb_multi: MongoDBMulti | MongoDB):
    tester = mongodb_multi.tester()
    tester.assert_connectivity()


def test_mdb_healthy_throughout_change_version(
    mdb_health_checker: MongoDBBackgroundTester,
):
    mdb_health_checker.assert_healthiness()
