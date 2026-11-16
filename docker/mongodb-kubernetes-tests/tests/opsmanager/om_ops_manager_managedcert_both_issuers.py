import pytest
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture
from tests.opsmanager.managedcert_helper import (
    ManagedCertificateNames,
    ManagedOpsManagerCertificateTests,
    _assert_appdb_client_resources_absent,
    _assert_current_certificates_ready,
    _configured_unused_client_issuer,
    _wait_for_appdb_convergence,
    _wait_for_unused_appdb_client_warning,
    cert_manager,
    customer_ca_issuer,
    default_operator,
    managed_ops_manager,
)

OM_NAME = "om-managedcert-both-issuers"
OM_CUSTOMER_ISSUER = f"{OM_NAME}-customer-ca"
APP_DB_CUSTOMER_ISSUER = f"{OM_NAME}-appdb-customer-ca"
UNUSED_CLIENT_ISSUER = f"{OM_NAME}-unused-client-issuer"


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    customer_ca_issuer(namespace, OM_CUSTOMER_ISSUER)
    customer_ca_issuer(namespace, APP_DB_CUSTOMER_ISSUER)
    return managed_ops_manager(
        OM_NAME,
        namespace,
        custom_version,
        custom_appdb_version,
        om_issuer=OM_CUSTOMER_ISSUER,
        appdb_issuer=APP_DB_CUSTOMER_ISSUER,
        unused_client_issuer=UNUSED_CLIENT_ISSUER,
    )


@pytest.mark.e2e_om_ops_manager_managedcert_both_issuers
class TestOpsManagerManagedCertificatesBothCustomIssuers(ManagedOpsManagerCertificateTests):
    def test_unused_appdb_client_configuration_is_reported(self, ops_manager: MongoDBOpsManager, namespace: str):
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        _wait_for_appdb_convergence(ops_manager, 3)
        _assert_current_certificates_ready(ops_manager)

        assert (
            _configured_unused_client_issuer(ops_manager) == UNUSED_CLIENT_ISSUER
        ), "the certificate readiness checks must run with the unused client issuer still configured"

        _wait_for_unused_appdb_client_warning(ops_manager)
        _assert_appdb_client_resources_absent(namespace, names)
