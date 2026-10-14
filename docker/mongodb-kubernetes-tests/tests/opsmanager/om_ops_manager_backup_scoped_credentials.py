"""
Backup that is enabled outside of the operator for a MongoDB replica set whose credentials are a project-scoped
(GROUP_OWNER) Ops Manager API key. Single-cluster counterpart of
tests/multicluster/multi_cluster_backup_scoped_credentials.py.

Flow: the replica set is deployed without spec.backup, backup is started the way the Ops Manager UI does it,
snapshotSchedule and assignmentLabels are then set in the CR (the reconcile fails because the key has no global role),
the labels are removed again and status.backup is expected to report the running backup.
"""

import random
import string

from kubetester import get_default_storage_class, try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.ops_manager.scoped_project import (
    ASSIGNMENT_LABELS,
    SNAPSHOT_SCHEDULE,
    ScopedProject,
    backup_config_status,
    create_scoped_project,
    start_backup_like_the_ui,
    wait_for_backup_config,
)

logger = test_logger.get_test_logger(__name__)

# Unique per run so that leftovers of a previous run in the same Ops Manager don't collide.
RUN_SUFFIX = "".join(random.choices(string.ascii_lowercase + string.digits, k=6))
RESOURCE_NAME = f"rs-scoped-{RUN_SUFFIX}"
BACKUP_STORE_NAME = "backup-store"


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_backup.yaml"), namespace=namespace)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    backup = resource["spec"]["backup"]
    del backup["s3Stores"]
    backup["members"] = 1
    backup["headDB"]["storageClass"] = get_default_storage_class()
    for store_type in ("opLogStores", "blockStores"):
        backup[store_type][0]["mongodbResourceRef"]["name"] = BACKUP_STORE_NAME
    try_load(resource)
    return resource


@fixture(scope="module")
def scoped_project(namespace: str, ops_manager: MongoDBOpsManager) -> ScopedProject:
    base_url = ops_manager.om_status().get_url()
    assert base_url is not None, "Ops Manager URL must not be None"
    return create_scoped_project(ops_manager.get_om_tester(), namespace, RESOURCE_NAME, base_url)


@fixture(scope="module")
def replica_set(
    namespace: str,
    custom_mdb_version: str,
    scoped_project: ScopedProject,
) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-for-om.yaml"), namespace=namespace, name=RESOURCE_NAME)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["members"] = 1
    resource["spec"]["opsManager"]["configMapRef"]["name"] = scoped_project.config_name
    resource["spec"]["credentials"] = scoped_project.secret_name
    try_load(resource)
    return resource


@mark.e2e_om_ops_manager_backup_scoped_credentials
class TestOpsManagerPreparedForBackup:
    def test_create_ops_manager(self, ops_manager: MongoDBOpsManager):
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900, ignore_errors=True)

    def test_create_backup_store(self, ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str):
        store = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"), namespace=namespace, name=BACKUP_STORE_NAME
        ).configure(ops_manager, "backup-infrastructure")
        store["spec"]["members"] = 1
        store.set_version(ensure_ent_version(custom_mdb_version))
        store.update()
        store.assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)


@mark.e2e_om_ops_manager_backup_scoped_credentials
class TestBackupEnabledOutsideOfOperator:
    def test_deploy_replica_set_without_backup(self, replica_set: MongoDB):
        assert "backup" not in replica_set["spec"]
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=900)

    def test_backup_is_not_started_yet(self, scoped_project: ScopedProject):
        wait_for_backup_config(scoped_project)
        assert backup_config_status(scoped_project) == "INACTIVE"

    def test_enable_backup_like_the_ui(self, scoped_project: ScopedProject):
        start_backup_like_the_ui(scoped_project)


@mark.e2e_om_ops_manager_backup_scoped_credentials
class TestConfiguringBackupInCrWithScopedKey:
    def test_snapshot_schedule_and_labels_in_cr_fail(self, replica_set: MongoDB):
        replica_set.load()
        replica_set.configure_backup(mode="enabled")
        replica_set["spec"]["backup"]["snapshotSchedule"] = SNAPSHOT_SCHEDULE
        replica_set["spec"]["backup"]["assignmentLabels"] = ASSIGNMENT_LABELS
        replica_set.update()

        replica_set.assert_reaches_phase(Phase.Failed, timeout=300)
        logger.info(f"CR failed with: {replica_set.get_status_message()}")
        logger.info(f"status of the resource while failed: {replica_set['status']}")

    def test_backup_still_running_in_om(self, scoped_project: ScopedProject):
        assert backup_config_status(scoped_project) == "STARTED"

    def test_remove_labels_from_cr(self, replica_set: MongoDB):
        replica_set.load()
        replica_set["spec"]["backup"]["assignmentLabels"] = None
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=600)

    def test_snapshot_schedule_applied_in_om(self, scoped_project: ScopedProject):
        schedule = scoped_project.tester.api_read_backup_snapshot_schedule()
        for key, value in SNAPSHOT_SCHEDULE.items():
            assert schedule[key] == value, f"{key}: expected {value}, OM has {schedule.get(key)}"

    def test_backup_status_is_reported_in_cr(self, replica_set: MongoDB):
        replica_set.load()
        logger.info(f"status of the resource: {replica_set['status']}")
        replica_set.assert_backup_reaches_status("STARTED", timeout=120)
