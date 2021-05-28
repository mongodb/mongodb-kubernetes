from typing import Optional

from kubetester import delete_deployment
from kubetester.kubetester import (
    skip_if_local,
    fixture as yaml_fixture,
)
from kubetester.mongodb import Phase
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str]) -> MongoDBOpsManager:
    """The fixture for Ops Manager to be created. Also results in a new s3 bucket
    created and used in OM spec"""
    om: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("upgrade_appdb.yaml"), namespace=namespace
    )
    # set no version so the bundled one is used
    om.set_appdb_version("")
    return om.create()


@mark.e2e_operator_upgrade_appdb_empty_version
def test_install_latest_official_operator(official_operator: Operator):
    official_operator.assert_is_running()


@mark.e2e_operator_upgrade_appdb_empty_version
def test_om_created(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600)
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=100)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)


@skip_if_local
def test_appdb_mongod_before_upgrade(ops_manager: MongoDBOpsManager):
    mdb_tester = ops_manager.get_appdb_tester()
    mdb_tester.assert_connectivity()

    # bundled version is 4.2.11-ent
    mdb_tester.assert_version("4.2.11")
    mdb_tester.assert_is_enterprise()


@skip_if_local
@mark.e2e_operator_upgrade_appdb_empty_version
def test_om_is_ok(ops_manager: MongoDBOpsManager):
    ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_operator_upgrade_appdb_empty_version
def test_delete_operator(operator_deployment_name: str, namespace: str):
    delete_deployment(namespace, operator_deployment_name)


@mark.e2e_operator_upgrade_appdb_empty_version
def test_change_appdb_version(ops_manager: MongoDBOpsManager):
    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["version"] = "4.2.11-ent"
    ops_manager.update()


@mark.e2e_operator_upgrade_appdb_empty_version
def test_upgrade_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_operator_upgrade_appdb_empty_version
def test_om_ok(ops_manager: MongoDBOpsManager):
    # status phases are updated gradually - we need to check for each of them (otherwise "check(Running) for OM"
    # will return True right away
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=100)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=500)

    ops_manager.get_om_tester().assert_healthiness()


@skip_if_local
@mark.e2e_operator_upgrade_appdb_empty_version
def test_appdb_mongod_after_upgrade(ops_manager: MongoDBOpsManager):
    mdb_tester = ops_manager.get_appdb_tester()
    mdb_tester.assert_connectivity()

    # we should have the same version afterwards
    mdb_tester.assert_version("4.2.11")
    mdb_tester.assert_is_enterprise()
