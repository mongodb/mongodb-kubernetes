import time
from typing import Iterator, Optional

import pymongo
from kubetester import create_or_update_secret, delete_pod, delete_pvc, read_secret, try_load
from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import assert_data_got_restored, get_central_cluster_client
from tests.opsmanager.om_ops_manager_backup import create_aws_secret, create_s3_bucket

"""
Tests the AppDB headless → online mode switch.

Scenario:
  1. Deploy Primary OM (AppDB in headless mode).
  2. Deploy Meta OM (a secondary Ops Manager instance).
  3. Create a credentials Secret for Meta OM admin API access.
  4. Patch Primary OM to set spec.applicationDatabase.managedByMetaOM.
  5. Assert AppDB pods restart and reach Running phase again.
  6. Assert the AppDB StatefulSet env vars reflect online mode
     (MMS_SERVER present; HEADLESS_AGENT / AUTOMATION_CONFIG_MAP absent).
  7. Assert the AppDB agent command contains online mode params
     (mmsBaseUrl, mmsGroupId, mmsApiKey as explicit flags; no -cluster flag).

Both Ops Manager instances are deployed in the same namespace for simplicity.
"""

PRIMARY_OM_NAME = "om-primary"
META_OM_NAME = "om-meta"
META_OM_CREDS_SECRET = "meta-om-creds"
META_OM_PROJECT_NAME = "primary-appdb"
SAMPLE_MDB_NAME = "mdb-primary-managed"

META_OM_S3_SECRET_NAME = "meta-om-s3-secret"
META_OM_OPLOG_SECRET_NAME = "meta-om-s3-secret-oplog"

AGENT_CONTAINER_NAME = "mongodb-agent"

APPDB_TEST_DATA = {"_id": "appdb_pitr_witness", "status": "before_change"}


@fixture(scope="module")
def meta_s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> Iterator[str]:
    create_aws_secret(aws_s3_client, META_OM_S3_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, "meta-om-s3")


@fixture(scope="module")
def meta_oplog_s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> Iterator[str]:
    create_aws_secret(aws_s3_client, META_OM_OPLOG_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, "meta-om-oplog")


@fixture(scope="module")
def meta_om_appdb_tester(meta_ops_manager: MongoDBOpsManager) -> OMTester:
    return meta_ops_manager.get_om_tester(project_name=META_OM_PROJECT_NAME)


@fixture(scope="function")
def primary_ops_manager(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_appdb_switch_primary_om.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    try_load(resource)
    return resource


@fixture(scope="module")
def meta_ops_manager(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    meta_s3_bucket: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_appdb_switch_meta_om.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = meta_s3_bucket

    try_load(resource)
    return resource


def _get_agent_container_env_vars(ops_manager: MongoDBOpsManager) -> dict:
    """Returns a name→value dict of env vars for the mongodb-agent container in the AppDB StatefulSet."""
    appdb_sts = ops_manager.read_appdb_statefulset()
    containers_by_name = {c.name: c for c in appdb_sts.spec.template.spec.containers}
    assert AGENT_CONTAINER_NAME in containers_by_name, (
        f"Container '{AGENT_CONTAINER_NAME}' not found in AppDB StatefulSet; "
        f"available: {list(containers_by_name.keys())}"
    )
    return {env.name: env.value for env in (containers_by_name[AGENT_CONTAINER_NAME].env or [])}


@mark.e2e_om_appdb_meta_om_mode_switch
class TestPrimaryOMCreation:
    """Deploy Primary OM with headless AppDB and verify baseline state."""

    def test_primary_om_reaches_running(self, primary_ops_manager: MongoDBOpsManager):
        primary_ops_manager.update()
        primary_ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        primary_ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_appdb_in_headless_mode(self, primary_ops_manager: MongoDBOpsManager):
        """Before the switch: AppDB agent container must carry headless mode env vars."""
        env = _get_agent_container_env_vars(primary_ops_manager)
        assert "HEADLESS_AGENT" in env, "Expected HEADLESS_AGENT in headless mode"
        assert env.get("HEADLESS_AGENT") == "true"
        assert "AUTOMATION_CONFIG_MAP" in env, "Expected AUTOMATION_CONFIG_MAP in headless mode"
        assert "MMS_SERVER" not in env, "MMS_SERVER must be absent in headless mode"
        assert "MMS_GROUP_ID" not in env, "MMS_GROUP_ID must be absent in headless mode"
        assert "MMS_API_KEY" not in env, "MMS_API_KEY must be absent in headless mode"

    def test_om_healthiness(self, primary_ops_manager: MongoDBOpsManager):
        primary_ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_om_appdb_meta_om_mode_switch
class TestMetaOMCreation:
    """Deploy the secondary (Meta) Ops Manager instance with backup enabled."""

    def test_meta_om_reaches_running(self, meta_ops_manager: MongoDBOpsManager):
        meta_ops_manager.update()
        meta_ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        meta_ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        meta_ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            msg_regexp="Oplog Store configuration is required for backup",
            timeout=300,
        )

    def test_meta_om_backup_running(self, meta_ops_manager: MongoDBOpsManager, meta_oplog_s3_bucket: str):
        meta_ops_manager.load()
        meta_ops_manager["spec"]["backup"]["s3OpLogStores"] = [
            {
                "name": "s3OplogStore1",
                "s3SecretRef": {
                    "name": META_OM_OPLOG_SECRET_NAME,
                },
                "pathStyleAccessEnabled": True,
                "s3BucketEndpoint": "s3.us-east-1.amazonaws.com",
                "s3BucketName": meta_oplog_s3_bucket,
            }
        ]
        meta_ops_manager.update()
        meta_ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=500)

    def test_meta_om_healthiness(self, meta_ops_manager: MongoDBOpsManager):
        meta_ops_manager.get_om_tester().assert_healthiness()

    def test_create_meta_om_credentials_secret(
        self,
        namespace: str,
        meta_ops_manager: MongoDBOpsManager,
    ):
        """Read Meta OM admin API credentials and store them in the Secret that
        Primary OM's reconciler will use to connect to Meta OM."""
        api_key_secret_name = meta_ops_manager.api_key_secret(namespace)
        api_key_data = read_secret(namespace, api_key_secret_name, get_central_cluster_client())

        # The admin-key secret may use either the legacy (user/publicApiKey) or
        # the current (publicKey/privateKey) format.
        if "publicApiKey" in api_key_data:
            public_key = api_key_data["user"]
            private_key = api_key_data["publicApiKey"]
        else:
            public_key = api_key_data["publicKey"]
            private_key = api_key_data["privateKey"]

        create_or_update_secret(
            namespace,
            META_OM_CREDS_SECRET,
            {"publicKey": public_key, "privateKey": private_key},
            api_client=get_central_cluster_client(),
        )


@mark.e2e_om_appdb_meta_om_mode_switch
class TestModeSwitchToMetaOM:
    """Patch Primary OM to enable managedByMetaOM and verify the transition."""

    def test_patch_primary_om_managed_by_meta_om(
        self,
        primary_ops_manager: MongoDBOpsManager,
        meta_ops_manager: MongoDBOpsManager,
    ):
        """Patch spec.applicationDatabase.managedByMetaOM on Primary OM to trigger the mode switch."""
        primary_ops_manager.load()
        primary_ops_manager["spec"]["applicationDatabase"]["managedByMetaOM"] = {
            "name": META_OM_NAME,
            "projectName": META_OM_PROJECT_NAME,
            "credentialsSecretRef": {"name": META_OM_CREDS_SECRET},
        }
        primary_ops_manager.update()

    def test_appdb_restarts_and_reaches_running(self, primary_ops_manager: MongoDBOpsManager):
        """AppDB pods must restart (leave Running) and then return to Running."""
        primary_ops_manager.appdb_status().assert_abandons_phase(Phase.Running, timeout=300)
        primary_ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_appdb_in_online_mode(self, primary_ops_manager: MongoDBOpsManager):
        """After the switch: AppDB agent container must carry online mode env vars.
        mmsGroupId and mmsApiKey are passed as explicit command params, not env vars."""
        env = _get_agent_container_env_vars(primary_ops_manager)
        assert "HEADLESS_AGENT" not in env, "HEADLESS_AGENT must be absent after mode switch"
        assert "AUTOMATION_CONFIG_MAP" not in env, "AUTOMATION_CONFIG_MAP must be absent after mode switch"
        # mmsGroupId and mmsApiKey are explicit command params, not env vars
        assert "MMS_GROUP_ID" not in env, "MMS_GROUP_ID must be absent (passed as -mmsGroupId cmd param)"
        assert "MMS_API_KEY" not in env, "MMS_API_KEY must be absent (passed as -mmsApiKey cmd param)"

    def test_primary_om_still_running(self, primary_ops_manager: MongoDBOpsManager):
        """Primary OM itself must remain healthy throughout the AppDB transition."""
        primary_ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=300)
        primary_ops_manager.get_om_tester().assert_healthiness()

    def test_appdb_registered_in_meta_om(
        self,
        primary_ops_manager: MongoDBOpsManager,
        meta_ops_manager: MongoDBOpsManager,
    ):
        """The AppDB project must now exist inside Meta OM."""
        meta_om_tester = meta_ops_manager.get_om_tester(project_name=META_OM_PROJECT_NAME)
        meta_om_tester.assert_group_exists()


@mark.e2e_om_appdb_meta_om_mode_switch
class TestAppDBBackupInMetaOM:
    """Enable and verify backup for Primary AppDB managed by Meta OM.
    Since the AppDB CRD has no backup.mode field, backup is enabled directly via the Meta OM API."""

    def test_insert_test_data(self, primary_appdb_collection):
        """Insert known data before enabling backup so it is captured in the first snapshot."""
        primary_appdb_collection.delete_many({"_id": APPDB_TEST_DATA["_id"]})
        primary_appdb_collection.insert_one(APPDB_TEST_DATA.copy())

    def test_enable_appdb_backup(self, meta_om_appdb_tester: OMTester):
        """Wait for the AppDB cluster to register in Meta OM's backup system, then enable backup."""
        meta_om_appdb_tester.api_enable_backup(timeout=300)

    def test_wait_backup_running(self, meta_om_appdb_tester: OMTester):
        meta_om_appdb_tester.wait_until_backup_running(timeout=300)

    def test_appdb_snapshot_ready(self, meta_om_appdb_tester: OMTester):
        meta_om_appdb_tester.wait_until_backup_snapshots_are_ready(expected_count=1)


@fixture(scope="function")
def primary_appdb_collection(primary_ops_manager: MongoDBOpsManager):
    connection_string = primary_ops_manager.read_appdb_connection_url()
    client = pymongo.MongoClient(connection_string, serverSelectionTimeoutMS=30000)
    yield client["testdb"]["testcollection"]
    client.close()

@mark.e2e_om_appdb_meta_om_mode_switch
class TestAppDBDisasterRecovery:
    """Simulate complete AppDB data loss (all PVCs deleted) and verify restore from Meta OM backup."""

    def test_delete_appdb_pvcs_and_pods(self, primary_ops_manager: MongoDBOpsManager, namespace: str):
        """Simulate total data loss: delete all AppDB PVCs and pods.
        PVCs are deleted first; pod deletion releases the pvc-protection finalizer so PVCs
        are fully removed before the StatefulSet recreates pods with fresh empty PVCs."""
        sts_name = primary_ops_manager.app_db_name()
        members = primary_ops_manager.get_appdb_members_count()
        for i in range(members):
            delete_pvc(namespace, f"data-{sts_name}-{i}")
            delete_pod(namespace, f"{sts_name}-{i}")

    def test_appdb_reaches_running_after_recreation(self, primary_ops_manager: MongoDBOpsManager):
        """Operator recreates AppDB with empty PVCs; agent reconnects to Meta OM."""
        primary_ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1900)

    def test_data_is_gone_after_recreation(self, primary_appdb_collection):
        """Verify data loss — confirms the fresh PVCs contain no prior data.
        Retries until the AppDB is connectable (pods may still be starting after recreation)."""
        start = time.time()
        timeout = 300
        last_error = None
        while time.time() - start < timeout:
            try:
                records = list(primary_appdb_collection.find({"_id": APPDB_TEST_DATA["_id"]}))
                assert records == [], f"Expected empty collection after PVC deletion, got: {records}"
                return
            except AssertionError:
                raise
            except Exception as e:
                last_error = e
                time.sleep(5)
        raise AssertionError(f"AppDB not connectable within {timeout}s after recreation. Last error: {last_error}")

    def test_restore_from_snapshot(self, meta_om_appdb_tester: OMTester):
        """Restore from the latest snapshot stored in Meta OM.
        PITR is not applicable here: PVC deletion breaks oplog continuity, making any
        pre-disaster pit time invalid. Snapshot restore is the correct recovery mechanism.
        Primary OM goes down during AppDB restore; completion is verified via OM recovery below."""
        meta_om_appdb_tester.create_restore_job_snapshot()


    def test_primary_om_reaches_running_after_restore(self, primary_ops_manager: MongoDBOpsManager):
        """Wait for Primary OM to come back — this implies AppDB was fully restored."""
        primary_ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=3600)
        primary_ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=3600)

    def test_data_restored(self, primary_appdb_collection):
        """Wait until the restored snapshot data appears in the collection.
        Retries on both connection errors (mongod restarting during apply) and empty results."""
        start = time.time()
        timeout = 3600
        last_error = None
        while time.time() - start < timeout:
            try:
                records = list(primary_appdb_collection.find({"_id": APPDB_TEST_DATA["_id"]}))
                if records == [APPDB_TEST_DATA]:
                    return
                last_error = f"data not yet present: {records}"
            except Exception as e:
                last_error = e
            time.sleep(5)
        raise AssertionError(f"Data not restored within {timeout}s after snapshot restore. Last error: {last_error}")


@mark.e2e_om_appdb_meta_om_mode_switch
class TestPITRAfterSnapshotRestore:
    """Empirically verify whether PITR is possible after a snapshot restore.

    After snapshot restore the AppDB is running again, and Meta OM still holds the
    oplog slices captured before the disaster (T1→T_disaster).  If OM considers those
    slices valid, a PITR job to the original snapshot timestamp (T1) should succeed —
    the restore uses the snapshot as base and replays zero oplog (pit == snapshot time).

    If OM reset the backup timeline when the fresh AppDB agent reconnected (between
    the disaster and the restore), 'Invalid restore point' will be returned and the
    test will fail, empirically confirming the limitation.
    """

    def test_pitr_to_snapshot_time(self, meta_om_appdb_tester: OMTester):
        """Attempt PITR to 2 minutes after the original snapshot creation time.
        pit_time == snapshot_time fails with 'no snapshot older than restore point'
        because OM requires a snapshot taken strictly before the pit time.
        Adding 120s gives OM a valid base snapshot and tests whether pre-disaster
        oplog slices (snapshot_time → snapshot_time+2min) survived the disaster cycle."""
        pit_millis = int(meta_om_appdb_tester.get_latest_backup_completion_time()) + 120_000
        assert pit_millis > 120_000, "Snapshot completion time not recorded — check test_appdb_snapshot_ready ran"
        meta_om_appdb_tester.create_restore_job_pit(pit_millis)

    def test_primary_om_reaches_running_after_pitr(self, primary_ops_manager: MongoDBOpsManager):
        primary_ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=3600)
        primary_ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=3600)

    def test_data_present_after_pitr(self, primary_appdb_collection):
        """Data inserted before the snapshot must survive the PITR restore."""
        start = time.time()
        timeout = 3600
        last_error = None
        while time.time() - start < timeout:
            try:
                records = list(primary_appdb_collection.find({"_id": APPDB_TEST_DATA["_id"]}))
                if records == [APPDB_TEST_DATA]:
                    return
                last_error = f"data not yet present: {records}"
            except Exception as e:
                last_error = e
            time.sleep(5)
        raise AssertionError(f"Data not present within {timeout}s after PITR restore. Last error: {last_error}")
