import pytest
from kubetester import try_load, wait_until
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture
from tests.opsmanager.managedcert_helper import (
    ManagedCertificateNames,
    ManagedOpsManagerCertificateTests,
    cert_manager,
    default_operator,
    managed_ops_manager,
)
from tests.tls.managedcert_helper import is_gone

OM_NAME = "om-managedcert"


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    return managed_ops_manager(OM_NAME, namespace, custom_version, custom_appdb_version)


def _managed_objects(names: ManagedCertificateNames) -> list[tuple[str, str]]:
    """What operator-managed certificates create for a self-signed Ops Manager and its AppDB.

    Backup is disabled in this suite, so it creates none of the backup objects.
    """
    return [
        ("certificate", names.om_leaf_certificate),
        ("certificate", names.om_ca_certificate),
        ("issuer", names.om_self_signed_issuer),
        ("issuer", names.om_ca_issuer),
        ("secret", names.om_leaf_certificate),
        ("secret", names.om_ca_certificate),
        ("secret", names.om_pem_secret),
        ("configmap", names.om_ca_bundle_config_map),
        ("certificate", names.app_db_leaf_certificate),
        ("certificate", names.app_db_ca_certificate),
        ("issuer", names.app_db_self_signed_issuer),
        ("issuer", names.app_db_ca_issuer),
        ("secret", names.app_db_leaf_certificate),
        ("secret", names.app_db_ca_certificate),
        ("secret", names.app_db_pem_secret),
        ("configmap", names.app_db_ca_bundle_config_map),
    ]


@pytest.mark.e2e_om_ops_manager_managedcert
class TestOpsManagerManagedCertificates(ManagedOpsManagerCertificateTests):
    def test_delete_removes_managed_certificates(self, ops_manager: MongoDBOpsManager, namespace: str):
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        objects = _managed_objects(names)
        missing = [f"{kind} {name}" for kind, name in objects if is_gone(kind, name, namespace)]
        assert not missing, f"the feature did not create: {missing}"

        ops_manager.delete()

        # A fresh object, the way is_gone checks a MongoDB. The fixture's ops_manager has already
        # loaded, and a 404 from it does not tell the wait which of the two checks is still failing.
        def everything_is_gone() -> bool:
            still_there = try_load(MongoDBOpsManager(name=ops_manager.name, namespace=namespace))
            return not still_there and all(is_gone(kind, name, namespace) for kind, name in objects)

        wait_until(everything_is_gone, timeout=300)
