"""
Single-cluster MongoDB replica set counterpart of multi_cluster_backup_scoped_credentials.py: backup is enabled
outside of the operator with a project-scoped (GROUP_OWNER) Ops Manager API key, then snapshotSchedule and
assignmentLabels are set in the CR (the reconcile fails), the labels are removed and status.backup is checked.

It re-uses the Ops Manager that the multi-cluster test (or multi_cluster_backup_restore) has already deployed
in the test namespace and does not install or restart anything: the operator must already be running.
"""

import random
import string

import kubernetes
import kubernetes.client
from kubetester import try_load
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

RUN_SUFFIX = "".join(random.choices(string.ascii_lowercase + string.digits, k=6))
RESOURCE_NAME = f"rs-scoped-{RUN_SUFFIX}"


@fixture(scope="module")
def ops_manager(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBOpsManager:
    """The already deployed Ops Manager, it is only loaded."""
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_backup.yaml"), namespace=namespace)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    assert try_load(resource), "Ops Manager has to be deployed in the namespace already"
    return resource


@fixture(scope="module")
def scoped_project(
    namespace: str,
    ops_manager: MongoDBOpsManager,
    central_cluster_client: kubernetes.client.ApiClient,
    multi_cluster_issuer_ca_configmap: str,
) -> ScopedProject:
    return create_scoped_project(
        ops_manager.get_om_tester(api_client=central_cluster_client),
        namespace,
        RESOURCE_NAME,
        ops_manager.om_status().get_url(),
        multi_cluster_issuer_ca_configmap,
        central_cluster_client,
    )


@fixture(scope="module")
def replica_set(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    custom_mdb_version: str,
    scoped_project: ScopedProject,
) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-for-om.yaml"), namespace=namespace, name=RESOURCE_NAME)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["members"] = 1
    resource["spec"]["opsManager"]["configMapRef"]["name"] = scoped_project.config_name
    resource["spec"]["credentials"] = scoped_project.secret_name
    try_load(resource)
    return resource


@mark.e2e_backup_scoped_credentials_single_cluster_rs
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

    def test_backup_works(self, scoped_project: ScopedProject):
        scoped_project.tester.wait_until_backup_snapshots_are_ready(expected_count=1)


@mark.e2e_backup_scoped_credentials_single_cluster_rs
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
