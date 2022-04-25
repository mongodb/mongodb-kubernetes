from typing import Optional

import pytest
import time
from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture
from datetime import datetime


@fixture(scope="module")
def ops_manager(
    namespace: str, custom_version: Optional[str], custom_appdb_version: str
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    return resource.create()


@pytest.mark.e2e_om_update_before_reconciliation
def test_create_om(ops_manager: MongoDBOpsManager):
    time.sleep(30)

    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["featureCompatibilityVersion"] = "4.2"
    ops_manager.update()

    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=300)
