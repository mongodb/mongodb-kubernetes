import os
from kubetester.certs import Certificate
from kubetester.certs import create_mongodb_tls_certs, create_ops_manager_tls_certs
from kubetester.kubetester import KubernetesTester, fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from typing import Optional
from pytest import fixture, mark

MDB_VERSION = "4.2.1"
CA_FILE_PATH_IN_TEST_POD = "/tests/ca.crt"
OM_NAME = "om-tls-monitored-appdb"


@fixture(scope="module")
def ops_manager_certs(namespace: str, issuer: str):
    return create_ops_manager_tls_certs(issuer, namespace, OM_NAME)


@fixture(scope="module")
def appdb_certs(namespace: str, issuer: str):
    create_mongodb_tls_certs(
        issuer, namespace, f"{OM_NAME}-db", "appdb-om-tls-monitored-appdb-db-cert"
    )
    return "appdb"


@fixture(scope="module")
@mark.usefixtures("appdb_certs", "ops_manager_certs", "issuer_ca_configmap")
def ops_manager(
    namespace: str,
    issuer_ca_configmap: str,
    appdb_certs: str,
    ops_manager_certs: str,
    custom_version: Optional[str],
) -> MongoDBOpsManager:

    print("Creating OM object")
    om = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_appdb_monitoring_tls.yaml"), namespace=namespace
    )
    om.set_version(custom_version)

    _copy_ca_into_test_pod(namespace, issuer_ca_configmap, CA_FILE_PATH_IN_TEST_POD)

    # ensure the requests library will use this CA when communicating with Ops Manager
    os.environ["REQUESTS_CA_BUNDLE"] = CA_FILE_PATH_IN_TEST_POD
    return om.create()


def _copy_ca_into_test_pod(
    namespace: str, configmap_name: str, ca_destination_path: str
):
    ca_contents = KubernetesTester.read_configmap(namespace, configmap_name)[
        "mms-ca.crt"
    ]
    with open(ca_destination_path, "w+") as f:
        f.write(ca_contents)


@mark.e2e_om_appdb_monitoring_tls
def test_om_created(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=100)
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_appdb_monitoring_tls
def test_appdb_group_is_monitored(ops_manager: MongoDBOpsManager):
    ops_manager.assert_appdb_monitoring_group_was_created()
    monitoring_metrics_are_being_sent = build_monitoring_agent_test_func(ops_manager)
    KubernetesTester.wait_until(monitoring_metrics_are_being_sent, timeout=120)


# @mark.e2e_om_appdb_monitoring_tls
# def test_enable_tls_on_appdb(ops_manager: MongoDBOpsManager):
#     ops_manager.load()
#     ops_manager["spec"]["applicationDatabase"]["security"] = {
#         "tls": {"ca": "issuer-ca", "secretRef": {"name": "certs-for-appdb"}}
#     }
#     ops_manager.update()
#
#     ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=100)
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


def build_monitoring_agent_test_func(ops_manager: MongoDBOpsManager):
    appdb_hosts = ops_manager.get_appdb_hosts()
    host_ids = [host["id"] for host in appdb_hosts]
    project_id = [host["groupId"] for host in appdb_hosts][0]
    tester = ops_manager.get_om_tester()

    def one_monitoring_agent_is_showing_metrics():
        for host_id in host_ids:
            measurements = tester.api_read_monitoring_measurements(
                host_id=host_id, database_name="admin", project_id=project_id
            )
            return measurements is not None and len(measurements) > 0

    return one_monitoring_agent_is_showing_metrics
