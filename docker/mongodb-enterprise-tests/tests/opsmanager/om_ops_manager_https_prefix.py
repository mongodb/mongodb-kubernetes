from typing import Optional

from kubetester.certs import create_ops_manager_tls_certs
from kubetester.kubetester import fixture as _fixture
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


@fixture(scope="module")
def ops_manager_certs(namespace: str, issuer: str):
    return create_ops_manager_tls_certs(issuer, namespace, "om-with-https", secret_name="prefix-om-with-https-cert")


@fixture(scope="module")
def ops_manager(
    namespace: str,
    issuer_ca_configmap: str,
    ops_manager_certs: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    om: MongoDBOpsManager = MongoDBOpsManager.from_yaml(_fixture("om_https_enabled.yaml"), namespace=namespace)
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)
    om["spec"]["security"] = {
        "certsSecretPrefix": "prefix",
        "tls": {
            "ca": issuer_ca_configmap,
        },
    }

    # No need to mount the additional mongods because they are not used
    # in this test, and make the test slower.
    om["spec"]["statefulSet"]["spec"]["template"]["spec"] = {}

    if is_multi_cluster():
        enable_multi_cluster_deployment(om)

    om.update()
    return om


@mark.e2e_om_ops_manager_https_enabled_prefix
def test_om_created(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    assert ops_manager.om_status().get_url().startswith("https://")
    assert ops_manager.om_status().get_url().endswith(":8443")
