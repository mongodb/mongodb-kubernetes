"""
CLOUDP-351614: Test that TLS can be disabled on AppDB without breaking monitoring.

This is a dedicated test file to verify that when TLS is disabled on AppDB,
the operator correctly clears stale TLS params from the monitoring configuration.
"""
from typing import Optional

from kubetester import create_or_update_secret, try_load
from kubetester.certs import create_ops_manager_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.cert.cert_issuer import create_appdb_certs
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment


def get_monitoring_tls_params(ops_manager: MongoDBOpsManager) -> dict:
    """
    Extract TLS-related additionalParams from monitoring config via OM API.

    Returns a dict with the additionalParams for all monitoring agents,
    keyed by hostname. An empty dict means no TLS params are present.

    Note: We use get_om_tester() to access the full automation config from
    Ops Manager API, which includes monitoringVersions. The K8s secret-based
    get_automation_config_tester() only has the cluster config for agents.
    """
    om_tester = ops_manager.get_om_tester(ops_manager.app_db_name())
    ac = om_tester.get_automation_config_tester()
    monitoring_versions = ac.automation_config.get("monitoringVersions", [])

    tls_params_by_host = {}
    for mv in monitoring_versions:
        hostname = mv.get("hostname", "unknown")
        additional_params = mv.get("additionalParams", {})
        if additional_params:
            tls_params_by_host[hostname] = additional_params

    return tls_params_by_host

OM_NAME = "om-tls-disable-test"
APPDB_NAME = f"{OM_NAME}-db"


@fixture(scope="module")
def ops_manager_certs(namespace: str, issuer: str):
    return create_ops_manager_tls_certs(issuer, namespace, OM_NAME)


@fixture(scope="module")
def appdb_certs(namespace: str, issuer: str):
    return create_appdb_certs(namespace, issuer, APPDB_NAME)


@fixture(scope="module")
@mark.usefixtures("appdb_certs", "ops_manager_certs", "issuer_ca_configmap")
def ops_manager(
    namespace: str,
    issuer_ca_configmap: str,
    appdb_certs: str,
    ops_manager_certs: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    """Create an Ops Manager with TLS-enabled AppDB for testing TLS disable."""
    om = MongoDBOpsManager.from_yaml(yaml_fixture("om_appdb_tls_disable.yaml"), namespace=namespace)
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)

    if try_load(om):
        return om

    if is_multi_cluster():
        enable_multi_cluster_deployment(om)

    om.update()
    return om


@mark.e2e_om_appdb_tls_disable
def test_om_created_with_tls(ops_manager: MongoDBOpsManager):
    """Verify OM and AppDB are running with TLS enabled."""
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_appdb_tls_disable
def test_appdb_monitoring_works_with_tls(ops_manager: MongoDBOpsManager):
    """Verify monitoring works with TLS enabled before we disable it."""
    ops_manager.assert_appdb_monitoring_group_was_created()
    ops_manager.assert_monitoring_data_exists(timeout=600, all_hosts=False)


@mark.e2e_om_appdb_tls_disable
def test_monitoring_config_has_tls_params_when_tls_enabled(ops_manager: MongoDBOpsManager):
    """
    CLOUDP-351614: Verify that monitoring config contains TLS params when TLS is enabled.

    When TLS is enabled on AppDB, the monitoring agents should be configured with
    TLS parameters in additionalParams, including:
    - useSslForAllConnections: "true"
    - sslTrustedServerCertificates: path to CA file
    - sslClientCertificate: path to client certificate (for x509 auth)
    """
    tls_params = get_monitoring_tls_params(ops_manager)

    # Monitoring agents should have TLS params configured
    assert len(tls_params) > 0, "Expected TLS params in monitoring config when TLS is enabled"

    # Verify TLS params contain expected keys
    for hostname, params in tls_params.items():
        assert params.get("useSslForAllConnections") == "true", (
            f"Expected useSslForAllConnections=true for {hostname}, got {params}"
        )
        assert "sslTrustedServerCertificates" in params, (
            f"Expected sslTrustedServerCertificates in params for {hostname}"
        )


@mark.e2e_om_appdb_tls_disable
def test_disable_tls_on_appdb(ops_manager: MongoDBOpsManager):
    """
    CLOUDP-351614: Disable TLS on AppDB and verify the operator correctly handles
    the transition without leaving stale TLS params in monitoring config.

    This test disables TLS by setting tls.enabled = False. The operator handles
    the TLS mode transition: requireTLS -> preferTLS -> allowTLS -> disabled.

    Note: We keep the certsSecretPrefix in place because:
    1. The cert files are needed during the TLS transition
    2. Removing certsSecretPrefix while TLS mode is still transitioning causes failures
    3. The certs are harmless to keep after TLS is disabled (just unused)
    """
    ops_manager.load()

    # Disable TLS mode (keeping certs for the transition)
    # The operator will handle the TLS mode transition: requireTLS -> preferTLS -> allowTLS -> disabled
    ops_manager["spec"]["applicationDatabase"]["security"]["tls"]["enabled"] = False
    ops_manager.update()

    # Wait for AppDB to reach Running state after TLS mode is fully disabled
    # Use a longer timeout as the transition goes through multiple TLS modes
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)


@mark.e2e_om_appdb_tls_disable
def test_monitoring_config_tls_params_cleared_after_tls_disabled(ops_manager: MongoDBOpsManager):
    """
    CLOUDP-351614: Verify that TLS params are CLEARED from monitoring config after TLS is disabled.

    THIS IS THE KEY TEST FOR THE BUG FIX:
    Before the fix in CLOUDP-351614, the operator would leave stale TLS params
    (useSslForAllConnections, sslClientCertificate, etc.) in the monitoring config
    even after TLS was disabled. This caused monitoring agents to fail because they
    would try to use TLS certificate files that are no longer valid for authentication.

    After the fix, the operator correctly clears these params by calling:
        delete(monitoringVersion, "additionalParams")
    """
    tls_params = get_monitoring_tls_params(ops_manager)

    # After TLS is disabled, monitoring config should NOT have TLS params
    # This assertion would have FAILED before the fix because stale TLS params remained
    assert len(tls_params) == 0, (
        f"CLOUDP-351614 BUG: TLS params should be cleared from monitoring config after "
        f"TLS is disabled, but found stale params: {tls_params}"
    )


@mark.e2e_om_appdb_tls_disable
def test_monitoring_works_after_tls_disable(ops_manager: MongoDBOpsManager):
    """
    CLOUDP-351614: Verify monitoring data can still be collected after TLS disable.

    After TLS is disabled, monitoring agents switch from x509 to SCRAM authentication.
    This test verifies that:
    1. The operator correctly cleared stale TLS params from monitoring config
    2. Monitoring agents can reconnect and collect data
    """
    # Use a longer timeout as monitoring agents need time to reconnect with SCRAM auth
    ops_manager.assert_monitoring_data_exists(timeout=1200, all_hosts=False)
