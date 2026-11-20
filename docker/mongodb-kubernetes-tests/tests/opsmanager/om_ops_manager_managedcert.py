import pytest
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture
from tests.opsmanager.managedcert_helper import (
    ManagedOpsManagerCertificateTests,
    cert_manager,
    default_operator,
    managed_ops_manager,
)

OM_NAME = "om-managedcert"


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    return managed_ops_manager(OM_NAME, namespace, custom_version, custom_appdb_version)


@pytest.mark.e2e_om_ops_manager_managedcert
class TestOpsManagerManagedCertificates(ManagedOpsManagerCertificateTests):
    pass
