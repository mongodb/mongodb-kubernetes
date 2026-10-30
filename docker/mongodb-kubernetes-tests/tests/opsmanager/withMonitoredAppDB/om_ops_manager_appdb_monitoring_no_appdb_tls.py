"""
E2E regression test for AppDB monitoring when Ops Manager is served over HTTPS with a custom CA
(`spec.security.tls.ca`) while the Application Database itself has no TLS.

The AppDB agent connects to Ops Manager's HTTPS endpoint to report monitoring data. The Ops
Manager CA ConfigMap is mounted into the AppDB pods, but before the fix nothing pointed the
agent's monitoring module at it, so it only trusted the system root CAs and every monitoring
connection failed with "x509: certificate signed by unknown authority" (the AppDB has no TLS
params, and the ones it had were cleared anyway).

Without the fix:
    - test_appdb_agent_trusts_om_ca fails: the agent command is missing -httpsCAFile.
    - test_appdb_group_is_monitored times out: no monitoring agent ever registers with Ops
      Manager, so no monitoring measurements show up either.

With the fix, the agent is started with -httpsCAFile=/mongodb-automation/certs/mms-ca.crt
pointing at the mounted Ops Manager CA, and monitoring reaches Ops Manager again.
"""
import os
from typing import Optional

from kubetester import create_or_update_secret, try_load
from kubetester.certs import create_ops_manager_tls_certs
from kubetester.kubetester import KubernetesTester, fixture as yaml_fixture
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

OM_NAME = "om-no-appdb-tls-monitored"
APPDB_NAME = f"{OM_NAME}-db"

AGENT_COMMAND_CMD = ["/bin/sh", "-c", "pgrep -f -a agent/mongodb-agent"]
OM_CA_AGENT_FLAG = "-httpsCAFile=/mongodb-automation/certs/mms-ca.crt"


@fixture(scope="module")
def ops_manager_certs(namespace: str, issuer: str):
    return create_ops_manager_tls_certs(issuer, namespace, OM_NAME)


@fixture(scope="module")
def ops_manager(
    namespace: str,
    issuer_ca_configmap: str,
    ops_manager_certs: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    issuer_ca_filepath: str,
) -> MongoDBOpsManager:
    print("Creating OM object")
    om = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_appdb_monitoring_no_appdb_tls.yaml"), namespace=namespace)
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)

    create_or_update_secret(namespace, "appdb-secret", {"password": "Hello-World!"})

    # ensure the requests library will use this CA when communicating with Ops Manager
    os.environ["REQUESTS_CA_BUNDLE"] = issuer_ca_filepath

    if is_multi_cluster():
        enable_multi_cluster_deployment(om)

    try_load(om)
    return om


@mark.e2e_om_appdb_monitoring_no_appdb_tls
def test_om_created(ops_manager: MongoDBOpsManager):
    ops_manager.update()
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    # Ops Manager must be served over HTTPS with the custom CA for this test to be meaningful
    om_url = ops_manager.om_status().get_url()
    assert om_url is not None
    assert om_url.startswith("https://")
    assert om_url.endswith(":8443")


@mark.e2e_om_appdb_monitoring_no_appdb_tls
def test_appdb_agent_trusts_om_ca(ops_manager: MongoDBOpsManager, namespace: str):
    """The agent command must point its monitoring module at the mounted Ops Manager CA."""
    for api_client, pod in ops_manager.read_appdb_pods():
        result = KubernetesTester.run_command_in_pod_container(
            pod.metadata.name,
            namespace,
            AGENT_COMMAND_CMD,
            container="mongodb-agent",
            api_client=api_client,
        )
        assert OM_CA_AGENT_FLAG in result, (
            "the AppDB agent must be started with -httpsCAFile pointing at the mounted "
            f"Ops Manager CA, got: {result}"
        )


@mark.e2e_om_appdb_monitoring_no_appdb_tls
def test_appdb_group_is_monitored(ops_manager: MongoDBOpsManager):
    """Monitoring data must reach Ops Manager: the monitoring module of the AppDB agent is the
    only client here that talks to Ops Manager over its custom-CA HTTPS endpoint without any
    other TLS configuration, so this fails before the agent trusts the Ops Manager CA."""
    ops_manager.assert_appdb_monitoring_group_was_created()
    ops_manager.assert_monitoring_data_exists()
