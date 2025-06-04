import time
from datetime import datetime
from typing import Optional

import pytest
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    resource.update()
    return resource


@pytest.mark.e2e_om_update_before_reconciliation
def test_create_om(ops_manager: MongoDBOpsManager, custom_appdb_version: str):
    time.sleep(30)

    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["featureCompatibilityVersion"] = ".".join(
        custom_appdb_version.split(".")[:2]
    )
    ops_manager.update()

    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
