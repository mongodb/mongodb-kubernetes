from os import environ

import pytest
from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture


@fixture(scope="module")
def ops_manager(namespace) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_basic.yaml"), namespace=namespace
    )
    if "CUSTOM_OM_VERSION" in environ:
        resource["spec"]["version"] = environ.get("CUSTOM_OM_VERSION")

    return resource.create()


@pytest.mark.e2e_om_weak_password
def test_update_secret_weak_password(ops_manager: MongoDBOpsManager):
    data = KubernetesTester.read_secret(
        ops_manager.namespace, ops_manager.get_admin_secret_name()
    )
    data["Password"] = "weak"
    KubernetesTester.update_secret(
        ops_manager.namespace, ops_manager.get_admin_secret_name(), data
    )


@pytest.mark.e2e_om_weak_password
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(
        Phase.Failed, msg_regexp=".*WEAK_PASSWORD.*", timeout=900
    )


@pytest.mark.e2e_om_weak_password
def test_fix_password(ops_manager: MongoDBOpsManager):
    data = KubernetesTester.read_secret(
        ops_manager.namespace, ops_manager.get_admin_secret_name()
    )
    data["Password"] = "Passw0rd."
    KubernetesTester.update_secret(
        ops_manager.namespace, ops_manager.get_admin_secret_name(), data
    )


@pytest.mark.e2e_om_weak_password
def test_om_reaches_running(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_abandons_phase(Phase.Failed)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=150)
