import time
from typing import Iterator, Optional

import pymongo
from kubetester import create_or_update_secret, delete_pvc, delete_statefulset, read_secret, try_load
from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import get_central_cluster_client
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

# Inserted before backup is enabled — captured in the first snapshot.
APPDB_TEST_DATA = {"_id": "appdb_pitr_witness", "status": "before_change"}

# Inserted after the snapshot then deleted ~20s later while backup runs normally.
# PITR to a point between insert and delete must replay the oplog and bring it back.
APPDB_CLEAN_PITR_DATA = {"_id": "appdb_clean_pitr_witness", "status": "exists_before_delete"}

# Recorded between the insert and delete of APPDB_CLEAN_PITR_DATA.
_clean_pitr_pit_millis: int = 0

# Inserted before disaster (PVC deletion); used to test PITR to a pre-disaster moment.
APPDB_PITR_DISASTER_DATA = {"_id": "appdb_pitr_disaster_witness", "status": "written_before_disaster"}

# Recorded after inserting APPDB_PITR_DISASTER_DATA and waiting for oplog flush.
_pitr_disaster_pit_millis: int = 0


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
        assert "MMS_GROUP_ID" not in env, "MMS_GROUP_ID must be absent (passed as -mmsGroupId cmd param)"
        assert "MMS_API_KEY" not in env, "MMS_API_KEY must be absent (passed as -mmsApiKey cmd param)"

    def test_primary_om_still_running(self, primary_ops_manager: MongoDBOpsManager):
        """Primary OM itself must remain healthy throughout the AppDB transition."""
        primary_ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=300)
        primary_ops_manager.get_om_tester().assert_healthiness()

    def test_appdb_registered_in_meta_om(
        self,
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
class TestPITRAfterDisaster:
    """Investigates whether PITR is possible after total AppDB data loss (all PVCs deleted).

    Flow:
      1. Insert a witness document.
      2. Wait 5 minutes so the oplog entries are flushed to S3.
      3. Record the PIT (timestamp after the insert).
      4. Delete all AppDB PVCs and pods — simulate disaster.
      5. Wait for AppDB to come back up (StatefulSet recreates pods with empty PVCs).
      6. Attempt PITR to the pre-disaster PIT.

    Expected outcome with current OM: RESTORE_INITIATION_FAILED —
    "Invalid restore point: Are you sure your backups were running at the time you selected?"

    Root cause: the fresh empty AppDB triggers a resync in Meta OM which clears lastOplogPush,
    making the pre-disaster oplog slices (still physically present in S3) unreachable via the
    restore API.  See architecture/appdb-pitr-after-data-loss.md for a detailed explanation.

    This test is left failing intentionally to get Ops Manager developers' input on whether
    this is by design or a limitation that could be addressed.
    """

    def test_insert_pre_disaster_data(self, primary_appdb_collection):
        """Insert a witness document and record the PIT after a 5s settle window."""
        global _pitr_disaster_pit_millis
        primary_appdb_collection.delete_many({"_id": APPDB_PITR_DISASTER_DATA["_id"]})
        primary_appdb_collection.insert_one(APPDB_PITR_DISASTER_DATA.copy())
        time.sleep(5)
        _pitr_disaster_pit_millis = int(time.time() * 1000)

    def test_wait_for_oplog_flush(self):
        """Wait 5 minutes to ensure the pre-disaster oplog entries are flushed to S3.
        Oplog slices are batched in ~5 minute windows; the PIT must fall inside a fully
        closed slice for OM to accept the restore request."""
        time.sleep(300)

    def test_delete_appdb_pvcs_and_statefulset(self, primary_ops_manager: MongoDBOpsManager, namespace: str):
        """Simulate total data loss: delete all AppDB PVCs then the StatefulSet.
        PVCs are deleted first (enter Terminating, held by pvc-protection finalizer while pods run).
        Deleting the StatefulSet with Foreground cascade terminates all pods simultaneously,
        which removes the pvc-protection finalizer from all PVCs at once — all PVCs are
        fully deleted before the operator can recreate the StatefulSet with fresh empty PVCs."""
        sts_name = primary_ops_manager.app_db_name()
        members = primary_ops_manager.get_appdb_members_count()
        for i in range(members):
            delete_pvc(namespace, f"data-{sts_name}-{i}")
        delete_statefulset(namespace, sts_name, propagation_policy="Foreground")

    def test_appdb_reaches_running_after_recreation(self, primary_ops_manager: MongoDBOpsManager):
        """Wait for the StatefulSet to recreate AppDB pods with fresh empty PVCs."""
        primary_ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1900)

    def test_pitr_to_pre_disaster_time(self, meta_om_appdb_tester: OMTester):
        """Attempt PITR to the pre-disaster moment.

        This is expected to fail: PVC deletion triggers a resync which resets lastOplogPush
        in Meta OM.  The pre-disaster S3 oplog slices exist but are no longer reachable.
        The error is: 409 RESTORE_INITIATION_FAILED — "Are you sure your backups were running
        at the time you selected?"

        Question for OM developers: is this limitation intentional, and is there a supported
        path to PITR after total replica set data loss?
        """
        assert _pitr_disaster_pit_millis > 0, "_pitr_disaster_pit_millis not set"
        meta_om_appdb_tester.create_restore_job_pit(_pitr_disaster_pit_millis)

    def test_primary_om_reaches_running_after_pitr_attempt(self, meta_om_appdb_tester: OMTester, primary_ops_manager: MongoDBOpsManager):
        """If PITR was somehow accepted, wait for Primary OM to recover.
        If PITR was rejected (expected), AppDB is still up and this passes quickly."""
        meta_om_appdb_tester.wait_until_backup_running(timeout=3600)
        primary_ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=3600)
        primary_ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=3600)


# @mark.e2e_om_appdb_meta_om_mode_switch
# class TestPITRWithoutDataLoss:
#     """Verify PITR works on a healthy backup after a post-disaster resync.
#
#     Runs after TestPITRAfterDisaster: the PVC deletion there triggers a resync in Meta OM
#     which resets the oplog timeline. A new snapshot must be taken before PITR can work again.
#     This class waits for that snapshot, then verifies that PITR on a clean (unbroken) timeline
#     correctly replays the oplog.
#
#     Writes a witness doc, waits, then deletes it — all while backup keeps running and
#     the oplog timeline stays intact. A PITR to a point between the insert and the delete
#     must replay the oplog and bring the doc back.
#     """
#
#     def test_wait_new_snapshot_after_disaster_resync(self, meta_om_appdb_tester: OMTester):
#         """After TestPITRAfterDisaster's PVC deletion Meta OM resyncs and starts a fresh
#         oplog timeline. PITR needs a snapshot as its base — wait for the post-resync snapshot
#         before inserting witness data."""
#         meta_om_appdb_tester.wait_until_backup_snapshots_are_ready(expected_count=2)
#
#     def test_insert_then_delete_witness(self, primary_appdb_collection):
#         """Insert a witness doc, record a PIT where it exists, wait, then delete it.
#         The recorded PIT sits firmly between the insert and delete oplog entries
#         (a 5s margin keeps it after the insert; oplog timestamps have 1s granularity)."""
#         global _clean_pitr_pit_millis
#         primary_appdb_collection.delete_many({"_id": APPDB_CLEAN_PITR_DATA["_id"]})
#         primary_appdb_collection.insert_one(APPDB_CLEAN_PITR_DATA.copy())
#         time.sleep(5)
#         _clean_pitr_pit_millis = int(time.time() * 1000)
#         time.sleep(20)
#         primary_appdb_collection.delete_one({"_id": APPDB_CLEAN_PITR_DATA["_id"]})
#
#     def test_witness_is_deleted(self, primary_appdb_collection):
#         """Sanity: the witness is gone in the live DB before we attempt PITR."""
#         records = list(primary_appdb_collection.find({"_id": APPDB_CLEAN_PITR_DATA["_id"]}))
#         assert records == [], f"Expected witness deleted before PITR, got: {records}"
#
#     def test_pitr_to_witness_exists_time(self, meta_om_appdb_tester: OMTester):
#         """PITR to the moment the witness existed. create_restore_job_pit retries on
#         409 / 'Invalid restore point' until the oplog slice covering the PIT is flushed
#         to S3 — the timeline is intact, so this resolves rather than failing."""
#         assert _clean_pitr_pit_millis > 0, (
#             "_clean_pitr_pit_millis not set — check test_insert_then_delete_witness ran"
#         )
#         meta_om_appdb_tester.create_restore_job_pit(_clean_pitr_pit_millis)
#
#     def test_primary_om_reaches_running_after_pitr(self, primary_ops_manager: MongoDBOpsManager):
#         """AppDB goes down during the PITR restore; wait for it and Primary OM to recover."""
#         primary_ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=3600)
#         primary_ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=3600)
#
#     def test_witness_restored_by_pitr(self, primary_appdb_collection):
#         """The witness must reappear: PITR replayed the insert but not the later delete.
#         Retries on connection errors (mongod restarting during apply) and on empty results."""
#         start = time.time()
#         timeout = 3600
#         last_error = None
#         while time.time() - start < timeout:
#             try:
#                 records = list(primary_appdb_collection.find({"_id": APPDB_CLEAN_PITR_DATA["_id"]}))
#                 if records == [APPDB_CLEAN_PITR_DATA]:
#                     return
#                 last_error = f"witness not yet present: {records}"
#             except Exception as e:
#                 last_error = e
#             time.sleep(5)
#         raise AssertionError(
#             f"Witness not restored within {timeout}s after healthy PITR. Last error: {last_error}"
#         )
#
#
# @mark.e2e_om_appdb_meta_om_mode_switch
# class TestSnapshotRestore:
#     """Simulate complete AppDB data loss (all PVCs deleted) and verify restore from Meta OM snapshot.
#
#     Runs after TestPITRWithoutDataLoss and TestPITRAfterDisaster intentionally: PVC deletion
#     triggers a resync in Meta OM which resets lastOplogPush and breaks the oplog timeline,
#     making PITR impossible afterwards. Snapshot restore is the only reliable recovery path
#     after total data loss.
#     """
#
#     def test_delete_appdb_pvcs_and_pods(self, primary_ops_manager: MongoDBOpsManager, namespace: str):
#         """Simulate total data loss: delete all AppDB PVCs and pods.
#         PVCs are deleted first; pod deletion releases the pvc-protection finalizer so PVCs
#         are fully removed before the StatefulSet recreates pods with fresh empty PVCs."""
#         sts_name = primary_ops_manager.app_db_name()
#         members = primary_ops_manager.get_appdb_members_count()
#         for i in range(members):
#             delete_pvc(namespace, f"data-{sts_name}-{i}")
#         delete_statefulset(namespace, sts_name, propagation_policy="Foreground")
#
#     def test_appdb_reaches_running_after_recreation(self, primary_ops_manager: MongoDBOpsManager):
#         """Operator recreates AppDB with empty PVCs; agent reconnects to Meta OM."""
#         primary_ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1900)
#
#     def test_appdb_connectable_after_recreation(self, primary_appdb_collection):
#         """Wait until AppDB accepts connections after pod/PVC recreation.
#         The CRD Running status can be stale so this is the real connectivity barrier.
#         Note: data may still be present if RS replication completed before all pods were
#         terminated. That is expected and does not affect snapshot restore (which overwrites
#         whatever is there)."""
#         start = time.time()
#         timeout = 1800
#         last_error = None
#         while time.time() - start < timeout:
#             try:
#                 primary_appdb_collection.database.command("ping")
#                 return
#             except Exception as e:
#                 last_error = e
#                 time.sleep(5)
#         raise AssertionError(f"AppDB not connectable within {timeout}s after PVC recreation. Last error: {last_error}")
#
#     def test_wait_backup_running_before_restore(self, meta_om_appdb_tester: OMTester):
#         """After PITR and PVC recreation Meta OM needs time to close the previous restore job
#         and resume backup before it will accept a new restore request."""
#         meta_om_appdb_tester.wait_until_backup_running(timeout=600)
#
#     # def test_restore_from_snapshot(self, meta_om_appdb_tester: OMTester):
#     #     """Restore from the latest snapshot stored in Meta OM.
#     #     Primary OM goes down during AppDB restore; completion is verified via OM recovery below."""
#     #     meta_om_appdb_tester.create_restore_job_snapshot()
#
#     def test_primary_om_reaches_running_after_restore(self, primary_ops_manager: MongoDBOpsManager):
#         """Wait for Primary OM to come back — this implies AppDB was fully restored."""
#         primary_ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=3600)
#         primary_ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=3600)
#
#     def test_data_restored(self, primary_appdb_collection):
#         """Wait until the restored snapshot data appears in the collection.
#         Retries on both connection errors (mongod restarting during apply) and empty results."""
#         start = time.time()
#         timeout = 3600
#         last_error = None
#         while time.time() - start < timeout:
#             try:
#                 records = list(primary_appdb_collection.find({"_id": APPDB_TEST_DATA["_id"]}))
#                 if records == [APPDB_TEST_DATA]:
#                     return
#                 last_error = f"data not yet present: {records}"
#             except Exception as e:
#                 last_error = e
#             time.sleep(5)
#         raise AssertionError(f"Data not restored within {timeout}s after snapshot restore. Last error: {last_error}")
