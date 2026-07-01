import time
from typing import Iterator, Optional

import pymongo
import kubernetes.client
from kubetester import create_or_update_secret, try_load
from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.opsmanager.om_ops_manager_backup import create_aws_secret, create_s3_bucket

"""
Tests the ExternalApplicationDatabaseRef feature end-to-end, including PITR.

Scenario:
  1. Deploy Primary OM with a managed AppDB (StatefulSet + PVCs).
  2. Deploy Meta OM with backup enabled (S3 + oplog stores).
  3. Patch Primary OM to add externalApplicationDatabaseRef:
       a. Create a Secret with the current AppDB connection string.
       b. The OpsManager controller removes its ownerRef from the AppDB StatefulSet
          and stops reconciling it; OM continues running with the same connection string.
  4. Deploy a MongoDB CR named identically to the AppDB StatefulSet (om-primary-db),
     configured to connect to Meta OM. The MongoDB controller takes ownership of the
     existing StatefulSet, reusing the same PVCs, and performs a rolling restart pod by pod.
  5. Enable backup for om-primary-db via Meta OM, wait for the first snapshot.
  6. Insert a witness document, wait for the oplog to flush to S3, record the PIT,
     then initiate PITR via Meta OM.
  7. Verify Primary OM recovers and the witness document is present after restore.
"""

OM_NAME = "om-primary"
APPDB_STS_NAME = f"{OM_NAME}-db"   # "om-primary-db" — MongoDB CR must use this name to reuse PVCs
EXTERNAL_CS_SECRET = "external-appdb-cs"
MONGODB_PROJECT_NAME = "external-appdb-project"

META_S3_SECRET_NAME = "meta-om-s3-secret"
META_OPLOG_SECRET_NAME = "meta-om-oplog-secret"

# Inserted before backup is enabled — captured in the first snapshot.
PRE_BACKUP_DATA = {"_id": "pre_backup_witness", "status": "before_backup"}

# Inserted after the first snapshot; used as the PITR witness.
PITR_WITNESS_DATA = {"_id": "pitr_witness", "status": "written_before_pitr"}
_pitr_pit_millis: int = 0


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    try_load(resource)
    return resource


@fixture(scope="module")
def meta_s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> Iterator[str]:
    create_aws_secret(aws_s3_client, META_S3_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, "ext-appdb-meta-s3")


@fixture(scope="module")
def meta_oplog_s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> Iterator[str]:
    create_aws_secret(aws_s3_client, META_OPLOG_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, "ext-appdb-meta-oplog")


@fixture(scope="module")
def meta_ops_manager(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    meta_s3_bucket: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_meta.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = meta_s3_bucket
    try_load(resource)
    return resource


@fixture(scope="module")
def external_appdb_tester(meta_ops_manager: MongoDBOpsManager) -> OMTester:
    return meta_ops_manager.get_om_tester(project_name=MONGODB_PROJECT_NAME)


@fixture(scope="module")
def external_mongodb(namespace: str, custom_appdb_version: str) -> MongoDB:
    resource: MongoDB = MongoDB.from_yaml(
        yaml_fixture("om_external_appdb_mongodb.yaml"), namespace=namespace
    )
    resource.set_version(custom_appdb_version)
    try_load(resource)
    return resource


@fixture(scope="function")
def primary_appdb_collection(ops_manager: MongoDBOpsManager):
    """Connects to the AppDB (om-primary-db) using the connection string stored by the operator.
    The secret om-primary-db-connection-string was created before the switch and remains valid
    because the connection string points to the same MongoDB RS."""
    connection_string = ops_manager.read_appdb_connection_url()
    client = pymongo.MongoClient(connection_string, serverSelectionTimeoutMS=30000)
    yield client["testdb"]["testcollection"]
    client.close()


@mark.e2e_om_external_appdb
class TestPrimaryOMCreation:
    """Deploy Primary OM with a managed AppDB and verify baseline state."""

    def test_om_reaches_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.update()
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_om_healthiness(self, ops_manager: MongoDBOpsManager):
        ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_om_external_appdb
class TestMetaOMCreation:
    """Deploy Meta OM with backup enabled; it will manage om-primary-db after the switch."""

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
                "s3SecretRef": {"name": META_OPLOG_SECRET_NAME},
                "pathStyleAccessEnabled": True,
                "s3BucketEndpoint": "s3.us-east-1.amazonaws.com",
                "s3BucketName": meta_oplog_s3_bucket,
            }
        ]
        meta_ops_manager.update()
        meta_ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=500)

    def test_meta_om_healthiness(self, meta_ops_manager: MongoDBOpsManager):
        meta_ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_om_external_appdb
class TestSwitchToExternalAppDB:
    """Patch OpsManager CR to add externalApplicationDatabaseRef.

    The OpsManager controller must remove its ownerRef from the AppDB StatefulSet
    and stop reconciling it, while OM continues running with the same connection string.
    """

    def test_create_external_appdb_secret(self, ops_manager: MongoDBOpsManager, namespace: str):
        """Create the Secret referenced by externalApplicationDatabaseRef, populated with the
        current AppDB connection string so OM continues connecting to the same MongoDB."""
        connection_string = ops_manager.read_appdb_connection_url()
        create_or_update_secret(namespace, EXTERNAL_CS_SECRET, {"connectionString.standard": connection_string})

    def test_patch_om_external_appdb_ref(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["externalApplicationDatabaseRef"] = {
            "connectionStringSecretRef": {"name": EXTERNAL_CS_SECRET}
        }
        ops_manager.update()

    def test_owner_ref_removed_from_appdb_sts(self, ops_manager: MongoDBOpsManager, namespace: str):
        """The OpsManager controller must remove its ownerRef from the AppDB StatefulSet
        so the MongoDB controller can claim it without conflict."""
        start = time.time()
        timeout = 120
        while time.time() - start < timeout:
            sts = kubernetes.client.AppsV1Api().read_namespaced_stateful_set(APPDB_STS_NAME, namespace)
            om_owners = [ref for ref in (sts.metadata.owner_references or []) if ref.name == ops_manager.name]
            if not om_owners:
                return
            time.sleep(5)
        raise AssertionError(
            f"OpsManager ownerRef not removed from AppDB StatefulSet {APPDB_STS_NAME} within {timeout}s"
        )

    def test_om_appdb_status_ok(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=300)

    def test_om_still_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=600)


@mark.e2e_om_external_appdb
class TestMongoDBCRTakesControl:
    """Deploy a MongoDB CR named identically to the AppDB StatefulSet.

    The MongoDB controller claims ownership of the existing StatefulSet (same PVCs),
    connects it to Meta OM, and performs a rolling restart pod by pod.
    """

    def test_deploy_mongodb_cr(self, external_mongodb: MongoDB, meta_ops_manager: MongoDBOpsManager):
        """Configure MongoDB CR to register with Meta OM, then deploy it.
        configure() creates the OM project ConfigMap and sets spec.credentials."""
        external_mongodb.configure(meta_ops_manager, MONGODB_PROJECT_NAME)
        external_mongodb.update()

    def test_mongodb_cr_reaches_running(self, external_mongodb: MongoDB):
        """MongoDB controller performs rolling restart pod by pod; CR reaches Running when done."""
        external_mongodb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_pvcs_preserved(self, namespace: str):
        """Verify that PVCs were not deleted during the StatefulSet ownership transfer."""
        pvc_api = kubernetes.client.CoreV1Api()
        for i in range(3):
            pvc = pvc_api.read_namespaced_persistent_volume_claim(f"data-{APPDB_STS_NAME}-{i}", namespace)
            assert pvc is not None, f"PVC data-{APPDB_STS_NAME}-{i} must survive the transition"

    def test_primary_om_still_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_primary_om_healthiness(self, ops_manager: MongoDBOpsManager):
        """After AppDB rolling restart OM may take time to re-establish its DB connection.
        Retry until the health endpoint returns 200 or the timeout expires."""
        start = time.time()
        timeout = 300
        last_error: Optional[AssertionError] = None
        while time.time() - start < timeout:
            try:
                ops_manager.get_om_tester().assert_healthiness()
                return
            except AssertionError as e:
                last_error = e
                time.sleep(10)
        raise last_error


@mark.e2e_om_external_appdb
class TestBackupSetup:
    """Enable backup for om-primary-db via Meta OM and wait for the first snapshot."""

    def test_insert_pre_backup_data(self, primary_appdb_collection):
        """Insert known data before enabling backup so it is captured in the first snapshot."""
        primary_appdb_collection.delete_many({"_id": PRE_BACKUP_DATA["_id"]})
        primary_appdb_collection.insert_one(PRE_BACKUP_DATA.copy())

    def test_enable_appdb_backup(self, external_mongodb: MongoDB):
        """Enable backup on the MongoDB CR so the operator configures the backup agent in Meta OM."""
        external_mongodb.load()
        external_mongodb.configure_backup(mode="enabled")
        external_mongodb.update()
        external_mongodb.assert_reaches_phase(Phase.Running, timeout=600)

    def test_wait_backup_running(self, external_appdb_tester: OMTester):
        external_appdb_tester.wait_until_backup_running(timeout=300)

    def test_first_snapshot_ready(self, external_appdb_tester: OMTester):
        external_appdb_tester.wait_until_backup_snapshots_are_ready(expected_count=1)


@mark.e2e_om_external_appdb
class TestPITR:
    """Verify PITR works for om-primary-db managed by Meta OM via ExternalApplicationDatabaseRef.

    Flow:
      1. Insert a witness document after the first snapshot has been taken.
      2. Wait 5 minutes so the oplog entries are flushed to S3.
      3. Record the PIT (timestamp after the insert).
      4. Initiate PITR to the recorded PIT via Meta OM.
      5. Wait for Primary OM to go down (om-primary-db is being restored) and recover.
      6. Verify the witness document is present in the restored database.
    """

    def test_insert_witness_data(self, primary_appdb_collection):
        """Insert a witness document and record the PIT after a 5s settle window."""
        global _pitr_pit_millis
        primary_appdb_collection.delete_many({"_id": PITR_WITNESS_DATA["_id"]})
        primary_appdb_collection.insert_one(PITR_WITNESS_DATA.copy())
        time.sleep(5)
        _pitr_pit_millis = int(time.time() * 1000)

    def test_wait_for_oplog_flush(self):
        """Wait 5 minutes to ensure oplog entries are flushed to S3.
        Oplog slices are batched in ~5 minute windows; the PIT must fall inside a fully
        closed slice for OM to accept the restore request."""
        time.sleep(300)

    def test_pitr_to_witness_time(self, external_appdb_tester: OMTester):
        """Initiate PITR to the moment the witness document was inserted.
        The PIT is after the first snapshot, so OM accepts the restore request."""
        assert _pitr_pit_millis > 0, "_pitr_pit_millis not set — check test_insert_witness_data ran"
        external_appdb_tester.create_restore_job_pit(_pitr_pit_millis)

    def test_primary_om_recovers_after_pitr(self, external_appdb_tester: OMTester, ops_manager: MongoDBOpsManager):
        """om-primary-db goes down during PITR restore; Primary OM loses its AppDB connection
        and enters an error state. Wait for restore to complete and Primary OM to recover."""
        external_appdb_tester.wait_until_backup_running(timeout=3600)
        ops_manager.om_status().assert_abandons_phase(Phase.Running, timeout=600)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=3600)

    def test_witness_data_restored(self, primary_appdb_collection):
        """Verify the witness document is present after PITR.
        The PIT was recorded after the insert, so PITR must have replayed the oplog entry."""
        start = time.time()
        timeout = 3600
        last_error = None
        while time.time() - start < timeout:
            try:
                records = list(primary_appdb_collection.find({"_id": PITR_WITNESS_DATA["_id"]}))
                if records == [PITR_WITNESS_DATA]:
                    return
                last_error = f"data not yet present: {records}"
            except Exception as e:
                last_error = e
            time.sleep(5)
        raise AssertionError(
            f"Witness document not restored within {timeout}s after PITR. Last error: {last_error}"
        )
