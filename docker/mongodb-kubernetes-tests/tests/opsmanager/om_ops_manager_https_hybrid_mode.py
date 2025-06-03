import time
from typing import Optional

from kubetester.certs import create_mongodb_tls_certs, create_ops_manager_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as _fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.cert.cert_issuer import create_appdb_certs
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


@fixture(scope="module")
def certs_secret_prefix(namespace: str, issuer: str):
    create_mongodb_tls_certs(issuer, namespace, "replicaset0", "certs-replicaset0-cert")
    return "certs"


@fixture(scope="module")
def appdb_certs(namespace: str, issuer: str) -> str:
    return create_appdb_certs(namespace, issuer, "om-with-https-db")


@fixture(scope="module")
def ops_manager_certs(namespace: str, issuer: str):
    return create_ops_manager_tls_certs(issuer, namespace, "om-with-https", secret_name="prefix-om-with-https-cert")


@fixture(scope="module")
def ops_manager(
    namespace: str,
    issuer_ca_configmap: str,
    appdb_certs: str,
    ops_manager_certs: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    om: MongoDBOpsManager = MongoDBOpsManager.from_yaml(_fixture("om_https_enabled.yaml"), namespace=namespace)
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)
    om.allow_mdb_rc_versions()
    del om["spec"]["statefulSet"]
    om["spec"]["security"] = {
        "certsSecretPrefix": "prefix",
        "tls": {
            "ca": issuer_ca_configmap,
        },
    }
    om["spec"]["configuration"]["automation.versions.source"] = "hybrid"
    om["spec"]["applicationDatabase"]["security"] = {
        "tls": {
            "ca": issuer_ca_configmap,
        },
        "certsSecretPrefix": appdb_certs,
    }

    if is_multi_cluster():
        enable_multi_cluster_deployment(om)

    om.update()
    return om


@fixture(scope="module")
def replicaset0(
    ops_manager: MongoDBOpsManager,
    namespace: str,
    custom_mdb_version: str,
    issuer_ca_configmap: str,
    certs_secret_prefix: str,
):
    """First replicaset to be created before Ops Manager is configured with HTTPS."""
    resource = MongoDB.from_yaml(_fixture("replica-set.yaml"), name="replicaset0", namespace=namespace).configure(
        ops_manager
    )
    resource.set_version(custom_mdb_version)
    resource.configure_custom_tls(issuer_ca_configmap, certs_secret_prefix)
    return resource.create()


@mark.e2e_om_ops_manager_https_enabled_hybrid
def test_appdb_running_over_tls(ops_manager: MongoDBOpsManager, ca_path: str):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
    ops_manager.get_appdb_tester(ssl=True, ca_path=ca_path).assert_connectivity()


@mark.e2e_om_ops_manager_https_enabled_hybrid
def test_om_reaches_running_state(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1000)


@mark.e2e_om_ops_manager_https_enabled_hybrid
def test_config_map_has_ca_set_correctly(ops_manager: MongoDBOpsManager, issuer_ca_plus: str, namespace: str):
    project1 = ops_manager.get_or_create_mongodb_connection_config_map("replicaset0", "replicaset0")
    data = {
        "sslMMSCAConfigMap": issuer_ca_plus,
    }
    KubernetesTester.update_configmap(namespace, project1, data)

    # Give a few seconds for the operator to catch the changes on
    # the project ConfigMaps
    time.sleep(10)


@mark.e2e_om_ops_manager_https_enabled_hybrid
def test_mongodb_replicaset_over_https_ops_manager(replicaset0: MongoDB, ca_path: str):
    replicaset0.assert_reaches_phase(Phase.Running, timeout=400, ignore_errors=True)
    replicaset0.assert_connectivity(ca_path=ca_path)
