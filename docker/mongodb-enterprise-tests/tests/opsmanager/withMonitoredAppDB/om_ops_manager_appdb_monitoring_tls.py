import os
from typing import Optional

import pymongo
from kubetester import create_or_update, create_or_update_secret
from kubetester.certs import create_ops_manager_tls_certs
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests.conftest import create_appdb_certs, is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

MDB_VERSION = "4.2.1"
CA_FILE_PATH_IN_TEST_POD = "/tests/ca.crt"
OM_NAME = "om-tls-monitored-appdb"
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
    issuer_ca_filepath: str,
) -> MongoDBOpsManager:
    create_or_update_secret(namespace, "appdb-secret", {"password": "Hello-World!"})

    print("Creating OM object")
    om = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_appdb_monitoring_tls.yaml"), namespace=namespace)
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)

    # ensure the requests library will use this CA when communicating with Ops Manager
    os.environ["REQUESTS_CA_BUNDLE"] = issuer_ca_filepath

    if is_multi_cluster():
        enable_multi_cluster_deployment(om)

    create_or_update(om)
    return om


@mark.e2e_om_appdb_monitoring_tls
def test_om_created(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_appdb_monitoring_tls
def test_appdb_group_is_monitored(ops_manager: MongoDBOpsManager):
    ops_manager.assert_appdb_monitoring_group_was_created()
    monitoring_metrics_are_being_sent = build_monitoring_agent_test_func(ops_manager)
    KubernetesTester.wait_until(monitoring_metrics_are_being_sent, timeout=120)


@mark.e2e_om_appdb_monitoring_tls
def test_appdb_password_can_be_changed(ops_manager: MongoDBOpsManager):
    # get measurements for the last minute, they should just work, as monitoring
    # is supposed to be working now.
    build_monitoring_agent_test_func(ops_manager, period="PT60S")()

    # Change the Secret containing the password
    data = {"password": "Hello-World!-new"}
    create_or_update_secret(
        ops_manager.namespace,
        ops_manager["spec"]["applicationDatabase"]["passwordSecretKeyRef"]["name"],
        data,
    )

    # We know that Ops Manager will detect the changes and be restarted
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=800)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_appdb_monitoring_tls
def test_new_database_is_monitored_after_restart(ops_manager: MongoDBOpsManager):
    # Connect with the new connection string
    connection_string = ops_manager.read_appdb_connection_url()
    client = pymongo.MongoClient(connection_string, tlsAllowInvalidCertificates=True)
    database_name = "new_database"
    database = client[database_name]
    collection = database["new_collection"]
    collection.insert_one({"witness": "database and collection should be created"})

    # We want to retrieve measurements from "new_database" which will indicate
    # that the monitoring agents are working with the new credentials.
    KubernetesTester.wait_until(
        build_monitoring_agent_test_func(ops_manager, database_name=database_name, period="PT100M"),
        timeout=120,
    )


# @mark.e2e_om_appdb_monitoring_tls
# def test_enable_tls_on_appdb(ops_manager: MongoDBOpsManager):
#     ops_manager.load()
#     ops_manager["spec"]["applicationDatabase"]["security"] = {
#         "tls": {"ca": "issuer-ca", "secretRef": {"name": "certs-for-appdb"}}
#     }
#     ops_manager.update()
#
#     ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


# @mark.e2e_om_appdb_monitoring_tls
# def test_monitoring_metrics_are_gathered(ops_manager: MongoDBOpsManager):
#     one_monitoring_agent_is_showing_metrics = build_monitoring_agent_test_func(
#         ops_manager
#     )
#
#     # some monitoring metrics should be reported within a few minutes
#     KubernetesTester.wait_until(one_monitoring_agent_is_showing_metrics, timeout=120)
#


def build_monitoring_agent_test_func(
    ops_manager: MongoDBOpsManager,
    database_name: str = "admin",
    period: str = "P1DT12H",
):
    """
    Returns a function that will check for existance of monitoring
    measurements in this Ops Manager instance.
    """
    appdb_hosts = ops_manager.get_appdb_hosts()
    host_ids = [host["id"] for host in appdb_hosts]
    project_id = [host["groupId"] for host in appdb_hosts][0]
    tester = ops_manager.get_om_tester()

    def one_monitoring_agent_is_showing_metrics():
        for host_id in host_ids:
            measurements = tester.api_read_monitoring_measurements(
                host_id,
                database_name=database_name,
                project_id=project_id,
                period=period,
            )
            return measurements is not None and len(measurements) > 0

    return one_monitoring_agent_is_showing_metrics
