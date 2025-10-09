import time
from typing import Optional

from kubetester.certs import create_ops_manager_tls_certs
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
def appdb_certs(namespace: str, issuer: str):
    return create_appdb_certs(namespace, issuer, "om-with-https-db")


@fixture(scope="module")
def ops_manager_certs(namespace: str, issuer: str):
    return create_ops_manager_tls_certs(issuer, namespace, "om-with-https", secret_name="prefix-om-with-https-cert")


@fixture(scope="module")
def ops_manager(
    namespace: str,
    issuer_ca_plus: str,
    appdb_certs: str,
    ops_manager_certs: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    om: MongoDBOpsManager = MongoDBOpsManager.from_yaml(_fixture("om_https_enabled.yaml"), namespace=namespace)
    om.set_version(custom_version)

    # do not use local mode
    del om["spec"]["configuration"]["automation.versions.source"]
    del om["spec"]["statefulSet"]

    om.set_appdb_version(custom_appdb_version)
    # configure CA + tls secrets for AppDB members to community with each other
    om["spec"]["applicationDatabase"]["security"] = {
        "tls": {"ca": issuer_ca_plus, "secretRef": {"prefix": appdb_certs}}
    }

    # configure the CA that will be used to communicate with Ops Manager
    om["spec"]["security"] = {
        "certsSecretPrefix": "prefix",
        "tls": {
            "ca": issuer_ca_plus,
        },
    }

    if is_multi_cluster():
        enable_multi_cluster_deployment(om)

    om.update()
    return om


@fixture(scope="module")
def replicaset0(ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_prev_version: str):
    resource = MongoDB.from_yaml(_fixture("replica-set.yaml"), name="replicaset0", namespace=namespace).configure(
        ops_manager, "replicaset0"
    )
    resource["spec"]["version"] = custom_mdb_prev_version

    return resource.create()


@fixture(scope="module")
def replicaset1(ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str):
    resource = MongoDB.from_yaml(_fixture("replica-set.yaml"), name="replicaset1", namespace=namespace).configure(
        ops_manager, "replicaset1"
    )
    resource["spec"]["version"] = custom_mdb_version

    return resource.create()


@mark.e2e_om_ops_manager_https_enabled_internet_mode
def test_enable_https_on_opsmanager(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    assert ops_manager.om_status().get_url().startswith("https://")
    assert ops_manager.om_status().get_url().endswith(":8443")

    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_ops_manager_https_enabled_internet_mode
def test_project_is_configured_with_custom_ca(
    ops_manager: MongoDBOpsManager,
    namespace: str,
    issuer_ca_plus: str,
):
    """Both projects are configured with the new HTTPS enabled Ops Manager."""
    project1 = ops_manager.get_or_create_mongodb_connection_config_map("replicaset0", "replicaset0")
    project2 = ops_manager.get_or_create_mongodb_connection_config_map("replicaset1", "replicaset1")

    data = {
        "sslMMSCAConfigMap": issuer_ca_plus,
    }
    KubernetesTester.update_configmap(namespace, project1, data)
    KubernetesTester.update_configmap(namespace, project2, data)

    # Give a few seconds for the operator to catch the changes on
    # the project ConfigMaps
    time.sleep(10)


@mark.e2e_om_ops_manager_https_enabled_internet_mode
def test_mongodb_replicaset_over_https_ops_manager(replicaset0: MongoDB, replicaset1: MongoDB):
    """Both replicasets get to running state and are reachable."""

    replicaset0.assert_reaches_phase(Phase.Running, timeout=360)
    replicaset0.assert_connectivity()

    replicaset1.assert_reaches_phase(Phase.Running, timeout=360)
    replicaset1.assert_connectivity()
