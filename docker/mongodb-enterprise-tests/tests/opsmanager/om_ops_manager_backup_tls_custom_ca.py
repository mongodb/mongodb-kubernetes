import os
import time
from typing import Optional

from pytest import mark, fixture

from kubetester import MongoDB, create_or_update
from kubetester.certs import create_ops_manager_tls_certs
from kubetester.kubetester import fixture as yaml_fixture, KubernetesTester
from kubetester.mongodb import Phase
from kubetester.opsmanager import MongoDBOpsManager
from tests.opsmanager.conftest import ensure_ent_version
from tests.opsmanager.om_ops_manager_backup import (
    OPLOG_RS_NAME,
    S3_SECRET_NAME,
    BLOCKSTORE_RS_NAME,
)
from tests.opsmanager.om_ops_manager_https import create_mongodb_tls_certs

OPLOG_SECRET_NAME = S3_SECRET_NAME + "-oplog"
FIRST_PROJECT_RS_NAME = "mdb-four-two"
SECOND_PROJECT_RS_NAME = "mdb-four-zero"

"""
This test checks the work with TLS-enabled backing databases (oplog & blockstore)
"""


@fixture(scope="module")
def ops_manager_certs(namespace: str, issuer: str):
    prefix = "prefix"
    create_ops_manager_tls_certs(
        issuer, namespace, "om-backup-tls", secret_name=f"{prefix}-om-backup-tls-cert"
    )
    return prefix


@fixture(scope="module")
def appdb_certs_secret(namespace: str, issuer: str):
    prefix = "appdb"
    create_mongodb_tls_certs(
        issuer, namespace, "om-backup-tls-db", f"{prefix}-om-backup-tls-db-cert"
    )
    return prefix


@fixture(scope="module")
def oplog_certs_secret(namespace: str, issuer: str):
    create_mongodb_tls_certs(
        issuer, namespace, OPLOG_RS_NAME, f"oplog-{OPLOG_RS_NAME}-cert"
    )
    return "oplog"


@fixture(scope="module")
def blockstore_certs_secret(namespace: str, issuer: str):
    create_mongodb_tls_certs(
        issuer, namespace, BLOCKSTORE_RS_NAME, f"blockstore-{BLOCKSTORE_RS_NAME}-cert"
    )
    return "blockstore"


@fixture(scope="module")
def first_project_certs(namespace: str, issuer: str):
    create_mongodb_tls_certs(
        issuer,
        namespace,
        FIRST_PROJECT_RS_NAME,
        f"first-project-{FIRST_PROJECT_RS_NAME}-cert",
    )
    return "first-project"


@fixture(scope="module")
def second_project_certs(namespace: str, issuer: str):
    create_mongodb_tls_certs(
        issuer,
        namespace,
        SECOND_PROJECT_RS_NAME,
        f"second-project-{SECOND_PROJECT_RS_NAME}-cert",
    )
    return "second-project"


@fixture(scope="module")
def ops_manager(
    namespace,
    issuer_ca_configmap: str,
    appdb_certs_secret: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    ops_manager_certs: str,
    issuer_ca_filepath: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup_tls.yaml"), namespace=namespace
    )

    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()
    resource["spec"]["security"]["certsSecretPrefix"] = ops_manager_certs

    # ensure the requests library will use this CA when communicating with Ops Manager
    os.environ["REQUESTS_CA_BUNDLE"] = issuer_ca_filepath

    # configure OM to be using hybrid mode
    resource["spec"]["configuration"]["automation.versions.source"] = "hybrid"

    create_or_update(resource)
    return resource


@fixture(scope="module")
def oplog_replica_set(
    ops_manager, issuer_ca_configmap: str, oplog_certs_secret: str
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=ops_manager.namespace,
        name=OPLOG_RS_NAME,
    ).configure(ops_manager, "oplog")
    resource.configure_custom_tls(issuer_ca_configmap, oplog_certs_secret)

    create_or_update(resource)
    return resource


@fixture(scope="module")
def blockstore_replica_set(
    ops_manager, issuer_ca_configmap: str, blockstore_certs_secret: str
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=ops_manager.namespace,
        name=BLOCKSTORE_RS_NAME,
    ).configure(ops_manager, "blockstore")
    resource.configure_custom_tls(issuer_ca_configmap, blockstore_certs_secret)

    create_or_update(resource)
    return resource


@mark.e2e_om_ops_manager_backup_tls_custom_ca
class TestOpsManagerCreation:
    def test_create_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

        # appdb rolling restart for configuring monitoring
        ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=200)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            msg_regexp="The MongoDB object .+ doesn't exist",
            timeout=900,
        )

    def test_add_custom_ca_to_project_configmap(
        self, ops_manager: MongoDBOpsManager, issuer_ca_plus: str, namespace: str
    ):
        projects = [
            ops_manager.get_or_create_mongodb_connection_config_map(
                OPLOG_RS_NAME, "oplog"
            ),
            ops_manager.get_or_create_mongodb_connection_config_map(
                BLOCKSTORE_RS_NAME, "blockstore"
            ),
            ops_manager.get_or_create_mongodb_connection_config_map(
                FIRST_PROJECT_RS_NAME, "firstProject"
            ),
            ops_manager.get_or_create_mongodb_connection_config_map(
                SECOND_PROJECT_RS_NAME, "secondProject"
            ),
        ]

        data = {
            "sslMMSCAConfigMap": issuer_ca_plus,
        }

        for project in projects:
            KubernetesTester.update_configmap(namespace, project, data)

        # Give a few seconds for the operator to catch the changes on
        # the project ConfigMaps
        time.sleep(10)

    def test_backing_dbs_created(
        self, blockstore_replica_set: MongoDB, oplog_replica_set: MongoDB
    ):
        oplog_replica_set.assert_reaches_phase(Phase.Running)
        blockstore_replica_set.assert_reaches_phase(Phase.Running)

    def test_oplog_running(self, oplog_replica_set: MongoDB, ca_path: str):
        oplog_replica_set.assert_connectivity(ca_path=ca_path)

    def test_blockstore_running(self, blockstore_replica_set: MongoDB, ca_path: str):
        blockstore_replica_set.assert_connectivity(ca_path=ca_path)

    def test_om_is_running(
        self,
        ops_manager: MongoDBOpsManager,
    ):
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=200)


@mark.e2e_om_ops_manager_backup_tls_custom_ca
class TestBackupForMongodb:
    """This part ensures that backup for the client works correctly and the snapshot is created."""

    @fixture(scope="class")
    def mdb_latest(
        self,
        ops_manager: MongoDBOpsManager,
        namespace,
        custom_mdb_version: str,
        issuer_ca_configmap: str,
        first_project_certs: str,
    ):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name=FIRST_PROJECT_RS_NAME,
        ).configure(ops_manager, "firstProject")
        # MongoD versions greater than 4.2.0 must be enterprise build to enable backup
        resource["spec"]["version"] = ensure_ent_version(custom_mdb_version)
        resource.configure_backup(mode="enabled")
        resource.configure_custom_tls(issuer_ca_configmap, first_project_certs)
        create_or_update(resource)
        return resource

    @fixture(scope="class")
    def mdb_prev(
        self,
        ops_manager: MongoDBOpsManager,
        namespace,
        custom_mdb_prev_version: str,
        issuer_ca_configmap: str,
        second_project_certs: str,
    ):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name=SECOND_PROJECT_RS_NAME,
        ).configure(ops_manager, "secondProject")
        resource["spec"]["version"] = ensure_ent_version(custom_mdb_prev_version)
        resource.configure_backup(mode="enabled")
        resource.configure_custom_tls(issuer_ca_configmap, second_project_certs)
        create_or_update(resource)
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
