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


@pytest.mark.e2e_om_weak_password
def test_update_secret_weak_password(ops_manager: MongoDBOpsManager):
    data = KubernetesTester.read_secret(ops_manager.namespace, ops_manager.get_admin_secret_name())
    data["Password"] = "weak"
    KubernetesTester.update_secret(ops_manager.namespace, ops_manager.get_admin_secret_name(), data)


@pytest.mark.e2e_om_weak_password
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Failed, msg_regexp=".*WEAK_PASSWORD.*", timeout=900)


@pytest.mark.e2e_om_weak_password
def test_fix_password(ops_manager: MongoDBOpsManager):
    data = KubernetesTester.read_secret(ops_manager.namespace, ops_manager.get_admin_secret_name())
    data["Password"] = "Passw0rd."
    KubernetesTester.update_secret(ops_manager.namespace, ops_manager.get_admin_secret_name(), data)


@pytest.mark.e2e_om_weak_password
def test_om_reaches_running(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_abandons_phase(Phase.Failed)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=150)
