"""
Backup that is enabled outside of the operator for a MongoDBMultiCluster whose credentials are a
project-scoped (GROUP_OWNER) Ops Manager API key.

Flow:
 1. a MongoDBMultiCluster is deployed without `spec.backup` (OM is prepared for backup, but backup is off),
 2. backup is started the way the Ops Manager UI does it (backupConfigs API with the project key),
 3. `spec.backup` with a snapshot schedule and `assignmentLabels` is added to the CR: the operator needs
    GLOBAL_READ_ONLY/GLOBAL_BACKUP_ADMIN for the labels, so the reconciliation fails with 401,
 4. `assignmentLabels` is removed from the CR: the resource gets deployed again, the snapshot schedule is applied
    and `status.backup` is expected to report the state of the running backup.
"""

import random
import string
from typing import List

import kubernetes
import kubernetes.client
from kubetester import get_default_storage_class, try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.omtester import OMTester
from kubetester.operator import Operator
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

# The Ops Manager with backup, oplog and blockstore is set up by the fixtures of the multi-cluster backup test.
from tests.multicluster.multi_cluster_backup_restore import (  # noqa: F401
    blockstore_replica_set,
    blockstore_user,
    oplog_replica_set,
    oplog_user,
    ops_manager,
    ops_manager_certs,
)

logger = test_logger.get_test_logger(__name__)

# Unique per run so that leftovers of a previous (possibly failed) run in the shared OM don't collide.
RUN_SUFFIX = "".join(random.choices(string.ascii_lowercase + string.digits, k=6))
RESOURCE_NAME = f"mdb-scoped-{RUN_SUFFIX}"


@fixture(scope="module")
def admin_om_tester(ops_manager: MongoDBOpsManager, central_cluster_client: kubernetes.client.ApiClient) -> OMTester:
    ops_manager.load()
    return ops_manager.get_om_tester(api_client=central_cluster_client)


@fixture(scope="module")
def scoped_project(
    namespace: str,
    admin_om_tester: OMTester,
    central_cluster_client: kubernetes.client.ApiClient,
    multi_cluster_issuer_ca_configmap: str,
    ops_manager: MongoDBOpsManager,
) -> ScopedProject:
    base_url = ops_manager.om_status().get_url()
    assert base_url is not None, "Ops Manager URL must not be None"
    return create_scoped_project(
        admin_om_tester, namespace, RESOURCE_NAME, base_url, multi_cluster_issuer_ca_configmap, central_cluster_client
    )


@fixture(scope="module")
def mongodb_multi(
    namespace: str,
    member_cluster_names: List[str],
    central_cluster_client: kubernetes.client.ApiClient,
    custom_mdb_version: str,
    scoped_project: ScopedProject,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(yaml_fixture("mongodb-multi.yaml"), RESOURCE_NAME, namespace)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["opsManager"]["configMapRef"]["name"] = scoped_project.config_name
    resource["spec"]["credentials"] = scoped_project.secret_name
    resource["spec"]["clusterSpecList"] = [
        {"clusterName": member_cluster_names[0], "members": 1},
        {"clusterName": member_cluster_names[1], "members": 1},
        {"clusterName": member_cluster_names[2], "members": 1},
    ]
    try_load(resource)
    return resource


@mark.e2e_multi_cluster_backup_scoped_credentials
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.wait_for_operator_ready()


@mark.e2e_multi_cluster_backup_scoped_credentials
class TestOpsManagerPreparedForBackup:
    def test_create_om(self, ops_manager: MongoDBOpsManager):
        ops_manager["spec"]["backup"]["headDB"]["storageClass"] = get_default_storage_class()
        ops_manager["spec"]["backup"]["members"] = 1
        ops_manager.update()
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending, msg_regexp="The MongoDB object .+ doesn't exist", timeout=1800
        )

    def test_backup_databases_created(
        self,
        oplog_replica_set: MongoDB,
        blockstore_replica_set: MongoDB,
        oplog_user,
    ):
        oplog_replica_set.update()
        oplog_replica_set.assert_reaches_phase(Phase.Running)
        blockstore_replica_set.update()
        blockstore_replica_set.assert_reaches_phase(Phase.Running)
        oplog_user.update()
        oplog_user.assert_reaches_phase(Phase.Updated)

    def test_om_backup_running(self, ops_manager: MongoDBOpsManager, oplog_user):
        ops_manager.load()
        ops_manager["spec"]["backup"]["opLogStores"][0]["mongodbUserRef"] = {"name": oplog_user.name}
        ops_manager.update()
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=300, ignore_errors=True)


@mark.e2e_multi_cluster_backup_scoped_credentials
class TestBackupEnabledOutsideOfOperator:
    def test_deploy_mongodb_multi_without_backup(self, mongodb_multi: MongoDBMulti):
        assert "backup" not in mongodb_multi["spec"]
        mongodb_multi.update()
        mongodb_multi.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=1200)

    def test_backup_is_not_started_yet(self, scoped_project: ScopedProject):
        wait_for_backup_config(scoped_project)
        assert backup_config_status(scoped_project) == "INACTIVE"

    def test_enable_backup_like_the_ui(self, scoped_project: ScopedProject):
        start_backup_like_the_ui(scoped_project)

    def test_backup_works(self, scoped_project: ScopedProject):
        scoped_project.tester.wait_until_backup_snapshots_are_ready(expected_count=1)

    def test_resource_unaffected_by_manual_backup(self, mongodb_multi: MongoDBMulti):
        mongodb_multi.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=300)


@mark.e2e_multi_cluster_backup_scoped_credentials
class TestConfiguringBackupInCrWithScopedKey:
    def test_snapshot_schedule_and_labels_in_cr_fail(self, mongodb_multi: MongoDBMulti):
        mongodb_multi.load()
        mongodb_multi.configure_backup(mode="enabled")
        mongodb_multi["spec"]["backup"]["snapshotSchedule"] = SNAPSHOT_SCHEDULE
        mongodb_multi["spec"]["backup"]["assignmentLabels"] = ASSIGNMENT_LABELS
        mongodb_multi.update()

        # GET/PUT /admin/backup/groups/{id} need GLOBAL_READ_ONLY / GLOBAL_BACKUP_ADMIN, a GROUP_OWNER key has neither
        mongodb_multi.assert_reaches_phase(Phase.Failed, timeout=300)
        message = mongodb_multi.get_status_message()
        assert message is not None
        logger.info(f"CR failed with: {message}")
        # the reason returned by Ops Manager must not be swallowed
        assert "Failed to configure backup" in message
        assert "401" in message

    def test_backup_still_running_in_om(self, scoped_project: ScopedProject):
        assert backup_config_status(scoped_project) == "STARTED"

    def test_remove_labels_from_cr(self, mongodb_multi: MongoDBMulti):
        mongodb_multi.load()
        # keys are removed from the merge patch by setting them to None
        mongodb_multi["spec"]["backup"]["assignmentLabels"] = None
        mongodb_multi.update()
        mongodb_multi.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=600)

    def test_snapshot_schedule_applied_in_om(self, scoped_project: ScopedProject):
        schedule = scoped_project.tester.api_read_backup_snapshot_schedule()
        for key, value in SNAPSHOT_SCHEDULE.items():
            assert schedule[key] == value, f"{key}: expected {value}, OM has {schedule.get(key)}"

    def test_backup_still_running_after_labels_removed(self, scoped_project: ScopedProject):
        assert backup_config_status(scoped_project) == "STARTED"

    def test_backup_status_is_reported_in_cr(self, mongodb_multi: MongoDBMulti):
        """Backup is up and running in OM, so the operator is expected to report it in status.backup."""
        mongodb_multi.load()
        logger.info(f"status of the resource: {mongodb_multi['status']}")
        mongodb_multi.assert_backup_reaches_status("STARTED", timeout=120)
