"""
Single-cluster MongoDB replica set counterpart of multi_cluster_backup_scoped_credentials.py: backup is enabled
outside of the operator with a project-scoped (GROUP_OWNER) Ops Manager API key, then snapshotSchedule and
assignmentLabels are set in the CR (the reconcile fails), the labels are removed and status.backup is checked.

It re-uses the Ops Manager that the multi-cluster test (or multi_cluster_backup_restore) has already deployed
in the test namespace and does not install or restart anything: the operator must already be running.
"""

import random
import string
from typing import Dict, Optional

import kubernetes
import kubernetes.client
from kubetester import create_or_update_configmap, create_or_update_secret, try_load
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMContext, OMTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests import test_logger

logger = test_logger.get_test_logger(__name__)

RUN_SUFFIX = "".join(random.choices(string.ascii_lowercase + string.digits, k=6))
RESOURCE_NAME = f"rs-scoped-{RUN_SUFFIX}"
ASSIGNMENT_LABELS = ["tenant-label"]

SNAPSHOT_SCHEDULE = {
    "snapshotIntervalHours": 12,
    "snapshotRetentionDays": 3,
    "dailySnapshotRetentionDays": 5,
    "weeklySnapshotRetentionWeeks": 4,
    "monthlySnapshotRetentionMonths": 5,
}


@fixture(scope="module")
def ops_manager(namespace: str, central_cluster_client: kubernetes.client.ApiClient) -> MongoDBOpsManager:
    """The already deployed Ops Manager, it is only loaded."""
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_backup.yaml"), namespace=namespace)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    assert try_load(resource), "Ops Manager has to be deployed in the namespace already"
    return resource


@fixture(scope="module")
def scoped_credentials(
    namespace: str,
    ops_manager: MongoDBOpsManager,
    central_cluster_client: kubernetes.client.ApiClient,
    multi_cluster_issuer_ca_configmap: str,
) -> Dict:
    """Organization, project and a GROUP_OWNER (project-scoped) API key wired into a configmap and a secret."""
    admin = ops_manager.get_om_tester(api_client=central_cluster_client)
    org = admin.om_request("post", "/orgs", json_object={"name": RESOURCE_NAME}).json()
    group = admin.om_request("post", "/groups", json_object={"name": RESOURCE_NAME, "orgId": org["id"]}).json()
    key = admin.om_request(
        "post",
        f"/groups/{group['id']}/apiKeys",
        json_object={"desc": "project scoped key for backup test", "roles": ["GROUP_OWNER"]},
    ).json()
    admin.om_request(
        "post",
        f"/orgs/{org['id']}/apiKeys/{key['id']}/accessList",
        json_object=[{"cidrBlock": "0.0.0.0/1"}, {"cidrBlock": "128.0.0.0/1"}],
    )

    secret_name = f"{RESOURCE_NAME}-credentials"
    create_or_update_secret(
        namespace,
        secret_name,
        {"publicKey": key["publicKey"], "privateKey": key["privateKey"]},
        api_client=central_cluster_client,
    )
    config_name = f"{RESOURCE_NAME}-config"
    create_or_update_configmap(
        namespace,
        config_name,
        {
            "baseUrl": ops_manager.om_status().get_url(),
            "projectName": RESOURCE_NAME,
            "orgId": org["id"],
            "sslMMSCAConfigMap": multi_cluster_issuer_ca_configmap,
        },
        api_client=central_cluster_client,
    )

    return {
        "group_id": group["id"],
        "secret_name": secret_name,
        "config_name": config_name,
        "tester": OMTester(
            OMContext(
                admin.context.base_url,
                key["publicKey"],
                key["privateKey"],
                project_name=RESOURCE_NAME,
                project_id=group["id"],
                org_id=org["id"],
            )
        ),
    }


@fixture(scope="module")
def replica_set(
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
    custom_mdb_version: str,
    scoped_credentials: Dict,
) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-for-om.yaml"), namespace=namespace, name=RESOURCE_NAME)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["members"] = 1
    resource["spec"]["opsManager"]["configMapRef"]["name"] = scoped_credentials["config_name"]
    resource["spec"]["credentials"] = scoped_credentials["secret_name"]
    try_load(resource)
    return resource


def backup_config_status(tester: OMTester) -> Optional[str]:
    configs = tester.api_read_backup_configs()
    return configs[0]["statusName"] if configs else None


@mark.e2e_backup_scoped_credentials_single_cluster_rs
class TestBackupEnabledOutsideOfOperator:
    def test_deploy_replica_set_without_backup(self, replica_set: MongoDB):
        assert "backup" not in replica_set["spec"]
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=900)

    def test_backup_is_not_started_yet(self, scoped_credentials: Dict):
        tester: OMTester = scoped_credentials["tester"]
        KubernetesTester.wait_until(lambda: len(tester.api_read_backup_configs()) > 0, timeout=300)
        assert backup_config_status(tester) == "INACTIVE"

    def test_enable_backup_like_the_ui(self, scoped_credentials: Dict):
        tester: OMTester = scoped_credentials["tester"]
        cluster_id = tester.get_backup_cluster_id()
        tester.om_request(
            "patch",
            f"/groups/{scoped_credentials['group_id']}/backupConfigs/{cluster_id}",
            json_object={"statusName": "STARTED"},
        )
        KubernetesTester.wait_until(lambda: backup_config_status(tester) == "STARTED", timeout=300)

    def test_backup_works(self, scoped_credentials: Dict):
        scoped_credentials["tester"].wait_until_backup_snapshots_are_ready(expected_count=1)


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

    def test_backup_still_running_in_om(self, scoped_credentials: Dict):
        assert backup_config_status(scoped_credentials["tester"]) == "STARTED"

    def test_remove_labels_from_cr(self, replica_set: MongoDB):
        replica_set.load()
        replica_set["spec"]["backup"]["assignmentLabels"] = None
        replica_set.update()
        replica_set.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=600)

    def test_snapshot_schedule_applied_in_om(self, scoped_credentials: Dict):
        schedule = scoped_credentials["tester"].api_read_backup_snapshot_schedule()
        for key, value in SNAPSHOT_SCHEDULE.items():
            assert schedule[key] == value, f"{key}: expected {value}, OM has {schedule.get(key)}"

    def test_backup_status_is_reported_in_cr(self, replica_set: MongoDB):
        replica_set.load()
        logger.info(f"status of the resource: {replica_set['status']}")
        replica_set.assert_backup_reaches_status("STARTED", timeout=120)
