from typing import Optional

from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import (
    skip_if_local,
    fixture as yaml_fixture,
    KubernetesTester,
)
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import MongoDBBackgroundTester
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark


@fixture(scope="module")
def ops_manager(namespace: str) -> MongoDBOpsManager:
    """The fixture for Ops Manager to be created. """
    om: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_full.yaml"), namespace=namespace
    )
    om.set_version("4.2.23")
    om["spec"]["backup"] = {"enabled": False}
    return om.create()


@fixture(scope="module")
def some_mdb(ops_manager: MongoDBOpsManager) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=ops_manager.namespace,
        name="some-mdb",
    ).configure(ops_manager, "someProject")
    resource["spec"]["version"] = "4.2.12"
    resource["spec"]["persistent"] = True

    return resource.create()


@fixture(scope="module")
def some_mdb_health_checker(some_mdb: MongoDB) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(some_mdb.tester(), allowed_sequential_failures=5)


@mark.e2e_operator_upgrade_unsupported_ops_manager
def test_install_latest_official_operator(official_operator: Operator):
    official_operator.assert_is_running()


@mark.e2e_operator_upgrade_unsupported_ops_manager
def test_om_created(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)


@skip_if_local
@mark.e2e_operator_upgrade_unsupported_ops_manager
def test_om_is_ok(ops_manager: MongoDBOpsManager):
    ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_operator_upgrade_unsupported_ops_manager
def test_mdb_created(some_mdb: MongoDB):
    some_mdb.assert_reaches_phase(Phase.Running)


# This is a part 2 of the Operator upgrade test. Upgrades the Operator the latest development one and checks
# that everything works


@mark.e2e_operator_upgrade_unsupported_ops_manager
def test_upgrade_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_operator_upgrade_unsupported_ops_manager
def test_start_mongod_background_tester(
    some_mdb_health_checker: MongoDBBackgroundTester,
):
    some_mdb_health_checker.start()


@mark.e2e_operator_upgrade_unsupported_ops_manager
def test_om_unsupported(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Unsupported, timeout=400)


@mark.e2e_operator_upgrade_unsupported_ops_manager
def test_replica_set_unsupported(some_mdb: MongoDB):
    some_mdb.assert_reaches_phase(Phase.Unsupported, timeout=400)


@mark.e2e_operator_upgrade_unsupported_ops_manager
def test_om_upgrade(ops_manager: MongoDBOpsManager):
    ops_manager.load()
    ops_manager["spec"]["version"] = "4.4.14"
    ops_manager.update()
    ops_manager.om_status().assert_abandons_phase(Phase.Unsupported, timeout=400)
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)


def test_replica_set_supported(some_mdb: MongoDB):
    some_mdb.assert_reaches_phase(Phase.Running, timeout=400)


@mark.e2e_operator_upgrade_unsupported_ops_manager
def test_some_mdb_ok(
    some_mdb: MongoDB, some_mdb_health_checker: MongoDBBackgroundTester
):
    some_mdb.assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)
    # The mongodb was supposed to be healthy all the time
    some_mdb_health_checker.assert_healthiness()
