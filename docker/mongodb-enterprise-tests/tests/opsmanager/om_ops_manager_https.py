import time
from typing import Optional

from kubetester import try_load
from kubetester.certs import create_ops_manager_tls_certs, rotate_cert
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as _fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests.conftest import create_appdb_certs, is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


@fixture(scope="module")
def appdb_certs(namespace: str, issuer: str) -> str:
    return create_appdb_certs(namespace, issuer, "om-with-https-db")


@fixture(scope="module")
def ops_manager_certs(namespace: str, issuer: str):
    return create_ops_manager_tls_certs(issuer, namespace, "om-with-https", "prefix-om-with-https-cert")


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

    if try_load(om):
        return om

    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)
    om.allow_mdb_rc_versions()

    if is_multi_cluster():
        enable_multi_cluster_deployment(om)

    return om


@fixture(scope="module")
def replicaset0(ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str):
    """The First replicaset to be created before Ops Manager is configured with HTTPS."""
    resource = MongoDB.from_yaml(_fixture("replica-set.yaml"), name="replicaset0", namespace=namespace).configure(
        ops_manager, "replicaset0"
    )

    resource.set_version(custom_mdb_version)

    try_load(resource)
    return resource


@fixture(scope="module")
def replicaset1(ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str):
    """Second replicaset to be created when Ops Manager was restarted with HTTPS."""
    resource = MongoDB.from_yaml(_fixture("replica-set.yaml"), name="replicaset1", namespace=namespace).configure(
        ops_manager, "replicaset1"
    )

    # NOTE: If running a test using a version different from 6.0.5 for OM6 means we will need to
    # also download the respective signature (tgz.sig) as seen in om_https_enabled.yaml
    resource.set_version(custom_mdb_version)

    try_load(resource)
    return resource


@mark.e2e_om_ops_manager_https_enabled
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.update()


@mark.e2e_om_ops_manager_https_enabled
def test_om_created_no_tls(ops_manager: MongoDBOpsManager):
    """Ops Manager is started over plain HTTP. AppDB also doesn't have TLS enabled"""

    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    assert ops_manager.om_status().get_url().startswith("http://")
    assert ops_manager.om_status().get_url().endswith(":8080")

    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_ops_manager_https_enabled
def test_appdb_running_no_tls(ops_manager: MongoDBOpsManager):
    ops_manager.get_appdb_tester().assert_connectivity()


@mark.e2e_om_ops_manager_https_enabled
def test_appdb_enable_tls(ops_manager: MongoDBOpsManager, issuer_ca_configmap: str, appdb_certs: str):
    """Enable TLS for the AppDB (not for OM though)."""
    ops_manager.load()
    ops_manager["spec"]["applicationDatabase"]["security"] = {
        "certsSecretPrefix": appdb_certs,
        "tls": {"ca": issuer_ca_configmap},
    }
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)


@mark.e2e_om_ops_manager_https_enabled
def test_appdb_running_over_tls(ops_manager: MongoDBOpsManager, ca_path: str):
    ops_manager.get_appdb_tester(ssl=True, ca_path=ca_path).assert_connectivity()


@mark.e2e_om_ops_manager_https_enabled
def test_appdb_no_connection_without_tls(ops_manager: MongoDBOpsManager):
    ops_manager.get_appdb_tester().assert_no_connection()


@mark.e2e_om_ops_manager_https_enabled
def test_replica_set_over_non_https_ops_manager(replicaset0: MongoDB):
    """First replicaset is started over non-HTTPS Ops Manager."""
    replicaset0.update()
    replicaset0.assert_reaches_phase(Phase.Running)
    replicaset0.assert_connectivity()


@mark.e2e_om_ops_manager_https_enabled
def test_enable_https_on_opsmanager(
    ops_manager: MongoDBOpsManager,
    issuer_ca_configmap: str,
    ops_manager_certs: str,
    custom_version: Optional[str],
):
    """Ops Manager is restarted with HTTPS enabled."""
    ops_manager["spec"]["security"] = {
        "certsSecretPrefix": "prefix",
        "tls": {"ca": issuer_ca_configmap},
    }

    # this enables download verification for om with https
    # probably need to be done above and if only test replicaset1 since that one already has tls setup or test below
    #  custom ca setup
    # only run this test if om > 6.0.18
    if custom_version >= "6.0.18":
        print("verifying download signature for OM!")
        ops_manager["spec"]["configuration"]["mms.featureFlag.automation.verifyDownloads"] = "enabled"

    ops_manager.update()

    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    assert ops_manager.om_status().get_url().startswith("https://")
    assert ops_manager.om_status().get_url().endswith(":8443")


@mark.e2e_om_ops_manager_https_enabled
def test_project_is_configured_with_custom_ca(
    ops_manager: MongoDBOpsManager,
    namespace: str,
    issuer_ca_configmap: str,
):
    """Both projects are configured with the new HTTPS enabled Ops Manager."""
    project1 = ops_manager.get_or_create_mongodb_connection_config_map("replicaset0", "replicaset0")
    project2 = ops_manager.get_or_create_mongodb_connection_config_map("replicaset1", "replicaset1")

    data = {
        "sslMMSCAConfigMap": issuer_ca_configmap,
    }
    KubernetesTester.update_configmap(namespace, project1, data)
    KubernetesTester.update_configmap(namespace, project2, data)

    # Give a few seconds for the operator to catch the changes on
    # the project ConfigMaps
    time.sleep(10)


@mark.e2e_om_ops_manager_https_enabled
def test_mongodb_replicaset_over_https_ops_manager(replicaset0: MongoDB, replicaset1: MongoDB):
    """Both replicasets get to running state and are reachable.
    Note that 'replicaset1' is created just now."""

    replicaset1.update()

    # This would fail if there are no, sig files provided for the respective mongodb which the agent downloads.
    replicaset1.assert_reaches_phase(Phase.Running, timeout=360)
    replicaset1.assert_connectivity()


@mark.e2e_om_ops_manager_https_enabled
def test_change_om_certificate_and_wait_for_running(ops_manager: MongoDBOpsManager, namespace: str):
    rotate_cert(namespace, certificate_name="prefix-om-with-https-cert")
    ops_manager.om_status().assert_abandons_phase(Phase.Running, timeout=600)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600)
    assert ops_manager.om_status().get_url().startswith("https://")
    assert ops_manager.om_status().get_url().endswith(":8443")


@mark.e2e_om_ops_manager_https_enabled
def test_change_appdb_certificate_and_wait_for_running(ops_manager: MongoDBOpsManager, namespace: str):
    rotate_cert(namespace, certificate_name="appdb-om-with-https-db-cert")
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=600)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_ops_manager_https_enabled
def test_change_om_certificate_with_sts_restarting(ops_manager: MongoDBOpsManager, namespace: str):
    ops_manager.trigger_om_sts_restart()
    rotate_cert(namespace, certificate_name="prefix-om-with-https-cert")
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    assert ops_manager.om_status().get_url().startswith("https://")
    assert ops_manager.om_status().get_url().endswith(":8443")


@mark.e2e_om_ops_manager_https_enabled
def test_change_appdb_certificate_with_sts_restarting(ops_manager: MongoDBOpsManager, namespace: str):
    ops_manager.trigger_appdb_sts_restart()
    rotate_cert(namespace, certificate_name="appdb-om-with-https-db-cert")
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
