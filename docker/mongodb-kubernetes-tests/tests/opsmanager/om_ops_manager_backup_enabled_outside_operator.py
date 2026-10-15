"""
Backup that is enabled outside of the operator, for example in the Ops Manager UI, for a MongoDB replica set.
Once `spec.backup` is added to the CR, `status.backup` is expected to report the state of the running backup.
Single-cluster counterpart of tests/multicluster/multi_cluster_backup_enabled_outside_operator.py.
"""

import random
import string

from kubetester import get_default_storage_class, try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger
from tests.common.ops_manager.backup_outside_operator import (
    backup_config_status,
    start_backup_like_the_ui,
    wait_for_backup_config,
)

logger = test_logger.get_test_logger(__name__)

# Unique per run so that leftovers of a previous run in the same Ops Manager don't collide.
RESOURCE_NAME = "rs-backup-" + "".join(random.choices(string.ascii_lowercase + string.digits, k=6))
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
def replica_set(namespace: str, ops_manager: MongoDBOpsManager, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"), namespace=namespace, name=RESOURCE_NAME
    ).configure(ops_manager, RESOURCE_NAME)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["members"] = 1
    try_load(resource)
    return resource


@fixture(scope="module")
def project(ops_manager: MongoDBOpsManager) -> OMTester:
    return ops_manager.get_om_tester(project_name=RESOURCE_NAME)


@mark.e2e_om_ops_manager_backup_enabled_outside_operator
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


@mark.e2e_om_ops_manager_backup_enabled_outside_operator
class TestBackupEnabledOutsideOfOperator:
    def test_deploy_replica_set_without_backup(self, replica_set: MongoDB):
        assert "backup" not in replica_set["spec"]
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=900)

    def test_backup_is_not_started_yet(self, project: OMTester):
        wait_for_backup_config(project)
        assert backup_config_status(project) == "INACTIVE"

    def test_enable_backup_like_the_ui(self, project: OMTester):
        start_backup_like_the_ui(project)


@mark.e2e_om_ops_manager_backup_enabled_outside_operator
class TestBackupEnabledInCr:
    def test_enable_backup_in_cr(self, replica_set: MongoDB):
        replica_set.load()
        replica_set.configure_backup(mode="enabled")
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=600)

    def test_backup_still_running_in_om(self, project: OMTester):
        assert backup_config_status(project) == "STARTED"

    def test_backup_status_is_reported_in_cr(self, replica_set: MongoDB):
        replica_set.load()
        logger.info(f"status of the resource: {replica_set['status']}")
        replica_set.assert_backup_reaches_status("STARTED", timeout=120)
