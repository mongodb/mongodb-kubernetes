import pytest
from pytest import fixture
from tests.opsmanager.om_ops_manager_queryable_backup_managedcert import (
    ManagedQueryableBackupTests,
    backed_up_mongodb,
    backing_databases,
    cert_manager,
    default_operator,
    issuer,
    ops_manager,
)


@fixture(scope="module")
def certificate_mode() -> str:
    return "issuer"


@pytest.mark.e2e_om_ops_manager_queryable_backup_managedcert_issuer
class TestCustomerIssuerManagedQueryableBackup(ManagedQueryableBackupTests):
    pass
