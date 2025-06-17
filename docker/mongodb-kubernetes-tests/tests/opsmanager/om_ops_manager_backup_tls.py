from typing import Optional

from kubetester import create_or_update_configmap
from kubetester.certs import create_mongodb_tls_certs
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.cert.cert_issuer import create_appdb_certs
from tests.conftest import get_member_cluster_api_client, is_multi_cluster
from tests.opsmanager.om_ops_manager_backup import (
    BLOCKSTORE_RS_NAME,
    OPLOG_RS_NAME,
    new_om_data_store,
)
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

"""
This test checks the work with TLS-enabled backing databases (oplog & blockstore)
"""


@fixture(scope="module")
def appdb_certs_secret(namespace: str, issuer: str):
    return create_appdb_certs(namespace, issuer, "om-backup-tls-db")


@fixture(scope="module")
def oplog_certs_secret(namespace: str, issuer: str):
    create_mongodb_tls_certs(issuer, namespace, OPLOG_RS_NAME, f"oplog-{OPLOG_RS_NAME}-cert")
    return "oplog"


@fixture(scope="module")
def blockstore_certs_secret(namespace: str, issuer: str):
    create_mongodb_tls_certs(issuer, namespace, BLOCKSTORE_RS_NAME, f"blockstore-{BLOCKSTORE_RS_NAME}-cert")
    return "blockstore"


@fixture(scope="module")
def ops_manager(
    namespace,
    ops_manager_issuer_ca_configmap: str,
    app_db_issuer_ca_configmap: str,
    appdb_certs_secret: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup_tls.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()
    resource["spec"]["security"]["tls"]["ca"] = ops_manager_issuer_ca_configmap
    resource["spec"]["applicationDatabase"]["security"]["tls"]["ca"] = app_db_issuer_ca_configmap
    resource["spec"]["backup"]["logging"] = {
        "LogBackAccessRef": {"name": "logback-access-config"},
        "LogBackRef": {"name": "logback-config"},
    }
    resource["spec"]["logging"] = {
        "LogBackAccessRef": {"name": "logback-access-config"},
        "LogBackRef": {"name": "logback-config"},
    }
    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    resource.update()
    return resource


@fixture(scope="module")
def oplog_replica_set(
    ops_manager, app_db_issuer_ca_configmap: str, oplog_certs_secret: str, custom_mdb_version: str
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=ops_manager.namespace,
        name=OPLOG_RS_NAME,
    ).configure(ops_manager, "development")
    resource.configure_custom_tls(app_db_issuer_ca_configmap, oplog_certs_secret)
    resource.set_version(ensure_ent_version(custom_mdb_version))

    resource.update()
    return resource


@fixture(scope="module")
def blockstore_replica_set(
    ops_manager, app_db_issuer_ca_configmap: str, blockstore_certs_secret: str, custom_mdb_version: str
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=ops_manager.namespace,
        name=BLOCKSTORE_RS_NAME,
    ).configure(ops_manager, "blockstore")
    resource.configure_custom_tls(app_db_issuer_ca_configmap, blockstore_certs_secret)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.update()
    return resource


@mark.e2e_om_ops_manager_backup_tls
class TestOpsManagerCreation:
    def test_create_logback_xml_configmap(self, namespace: str, custom_logback_file_path: str, central_cluster_client):
        logback = open(custom_logback_file_path).read()
        data = {"logback.xml": logback}
        create_or_update_configmap(namespace, "logback-config", data, api_client=central_cluster_client)

    def test_create_logback_access_xml_configmap(
        self, namespace: str, custom_logback_file_path: str, central_cluster_client
    ):
        logback = open(custom_logback_file_path).read()
        data = {"logback-access.xml": logback}
        create_or_update_configmap(namespace, "logback-access-config", data, api_client=central_cluster_client)

    def test_create_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(Phase.Running)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)

        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            msg_regexp="The MongoDB object .+ doesn't exist",
            timeout=900,
        )

    def test_backing_dbs_created(self, oplog_replica_set: MongoDB, blockstore_replica_set: MongoDB):
        oplog_replica_set.assert_reaches_phase(Phase.Running)
        blockstore_replica_set.assert_reaches_phase(Phase.Running)

    def test_oplog_running(self, oplog_replica_set: MongoDB, ca_path: str):
        oplog_replica_set.assert_connectivity(ca_path=ca_path)

    def test_blockstore_running(self, blockstore_replica_set: MongoDB, ca_path: str):
        blockstore_replica_set.assert_connectivity(ca_path=ca_path)

    def test_om_is_running(
        self,
        ops_manager: MongoDBOpsManager,
        oplog_replica_set: MongoDB,
        blockstore_replica_set: MongoDB,
    ):
        ops_manager.backup_status().assert_reaches_phase(Phase.Running)
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        om_tester.assert_oplog_stores([new_om_data_store(oplog_replica_set, "oplog1")])
        om_tester.assert_block_stores([new_om_data_store(blockstore_replica_set, "blockStore1")])

    def test_logback_xml_mounted_correctly_om(self, ops_manager: MongoDBOpsManager, namespace):
        cmd_normal = [
            "/bin/sh",
            "-c",
            "cat /mongodb-ops-manager/conf/logback.xml",
        ]
        cmd_access = [
            "/bin/sh",
            "-c",
            "cat /mongodb-ops-manager/conf/logback-access.xml",
        ]

        for member_cluster_name, pod_name in ops_manager.get_om_pod_names_in_member_clusters():
            member_api_client = get_member_cluster_api_client(member_cluster_name)
            result = KubernetesTester.run_command_in_pod_container(
                pod_name, namespace, cmd_normal, container="mongodb-ops-manager", api_client=member_api_client
            )
            assert "<!--everything here is custom and DEBUG is set to INFO-->" in result
            result = KubernetesTester.run_command_in_pod_container(
                pod_name, namespace, cmd_access, container="mongodb-ops-manager", api_client=member_api_client
            )
            assert "<!--everything here is custom and DEBUG is set to INFO-->" in result

    def test_logback_xml_mounted_correctly_backup(self, ops_manager: MongoDBOpsManager, namespace):
        cmd_normal = [
            "/bin/sh",
            "-c",
            "cat /mongodb-ops-manager/conf/logback.xml",
        ]
        cmd_access = [
            "/bin/sh",
            "-c",
            "cat /mongodb-ops-manager/conf/logback-access.xml",
        ]

        for member_cluster_name, pod_name in ops_manager.backup_daemon_pod_names():
            member_api_client = get_member_cluster_api_client(member_cluster_name)
            result = KubernetesTester.run_command_in_pod_container(
                pod_name, namespace, cmd_normal, container="mongodb-backup-daemon", api_client=member_api_client
            )
            assert "<!--everything here is custom and DEBUG is set to INFO-->" in result
            result = KubernetesTester.run_command_in_pod_container(
                pod_name, namespace, cmd_access, container="mongodb-backup-daemon", api_client=member_api_client
            )
            assert "<!--everything here is custom and DEBUG is set to INFO-->" in result


@mark.e2e_om_ops_manager_backup_tls
class TestBackupForMongodb:
    """This part ensures that backup for the client works correctly and the snapshot is created."""

    @fixture(scope="class")
    def mdb_latest(self, ops_manager: MongoDBOpsManager, namespace, custom_mdb_version: str):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name="mdb-four-two",
        ).configure(ops_manager, "firstProject")
        # MongoD versions greater than 4.2.0 must be enterprise build to enable backup
        resource.set_version(ensure_ent_version(custom_mdb_version))
        resource.configure_backup(mode="enabled")
        resource.update()
        return resource

    @fixture(scope="class")
    def mdb_prev(self, ops_manager: MongoDBOpsManager, namespace, custom_mdb_prev_version: str):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name="mdb-four-zero",
        ).configure(ops_manager, "secondProject")
        resource.set_version(ensure_ent_version(custom_mdb_prev_version))
        resource.configure_backup(mode="enabled")
        resource.update()
        return resource

    def test_mdbs_created(self, mdb_latest: MongoDB, mdb_prev: MongoDB):
        mdb_latest.assert_reaches_phase(Phase.Running)
        mdb_prev.assert_reaches_phase(Phase.Running)

    def test_mdbs_backed_up(self, ops_manager: MongoDBOpsManager):
        om_tester_first = ops_manager.get_om_tester(project_name="firstProject")
        om_tester_second = ops_manager.get_om_tester(project_name="secondProject")

        # wait until a first snapshot is ready for both
        om_tester_first.wait_until_backup_snapshots_are_ready(expected_count=1)
        om_tester_second.wait_until_backup_snapshots_are_ready(expected_count=1)
