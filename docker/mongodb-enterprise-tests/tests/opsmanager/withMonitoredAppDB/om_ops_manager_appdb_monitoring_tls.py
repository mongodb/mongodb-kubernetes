import os
from typing import Optional

import pymongo
from kubetester import create_or_update_secret
from kubetester.certs import create_ops_manager_tls_certs
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests.conftest import create_appdb_certs, is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

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

    om.update()
    return om


@mark.e2e_om_appdb_monitoring_tls
def test_om_created(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
    ops_manager.assert_appdb_preferred_hostnames_are_added()
    ops_manager.assert_appdb_hostnames_are_correct()


@mark.e2e_om_appdb_monitoring_tls
def test_appdb_group_is_monitored(ops_manager: MongoDBOpsManager):
    ops_manager.assert_appdb_monitoring_group_was_created()
    ops_manager.assert_monitoring_data_exists()


@mark.e2e_om_appdb_monitoring_tls
def test_appdb_password_can_be_changed(ops_manager: MongoDBOpsManager):
    # Change the Secret containing the password
    data = {"password": "Hello-World!-new"}
    create_or_update_secret(
        ops_manager.namespace,
        ops_manager["spec"]["applicationDatabase"]["passwordSecretKeyRef"]["name"],
        data,
    )

    # We know that Ops Manager will detect the changes and be restarted
    ops_manager.appdb_status().assert_reaches_phase(Phase.Pending, timeout=120)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
    ops_manager.om_status().assert_reaches_phase(Phase.Pending, timeout=120)
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=800)


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
    ops_manager.assert_monitoring_data_exists(database_name=database_name, timeout=1200, all_hosts=False)
