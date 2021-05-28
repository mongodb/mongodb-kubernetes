from typing import Optional

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
    om.set_version(custom_version)
    return om.create()


@mark.e2e_operator_upgrade_appdb
def test_install_latest_official_operator(official_operator: Operator):
    official_operator.assert_is_running()


@mark.e2e_operator_upgrade_appdb
def test_om_created(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Reconciling, timeout=100)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=400)


@skip_if_local
@mark.e2e_operator_upgrade_appdb
def test_om_is_ok(ops_manager: MongoDBOpsManager):
    ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_operator_upgrade_appdb
def test_upgrade_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_operator_upgrade_appdb
def test_om_ok(ops_manager: MongoDBOpsManager):
    # status phases are updated gradually - we need to check for each of them (otherwise "check(Running) for OM"
    # will return True right away
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=100)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1200)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=500)

    ops_manager.get_om_tester().assert_healthiness()
