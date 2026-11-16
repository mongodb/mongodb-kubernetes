import pytest
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture
from tests.opsmanager.managedcert_helper import (
    ManagedOpsManagerCertificateTests,
    cert_manager,
    customer_ca_issuer,
    default_operator,
    managed_ops_manager,
)

OM_NAME = "om-managedcert-om-issuer"
CUSTOMER_ISSUER = f"{OM_NAME}-customer-ca"


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    customer_ca_issuer(namespace, CUSTOMER_ISSUER)
    return managed_ops_manager(
        OM_NAME,
        namespace,
        custom_version,
        custom_appdb_version,
        om_issuer=CUSTOMER_ISSUER,
    )


@pytest.mark.e2e_om_ops_manager_managedcert_om_issuer
class TestOpsManagerManagedCertificatesCustomOMIssuer(ManagedOpsManagerCertificateTests):
    pass
