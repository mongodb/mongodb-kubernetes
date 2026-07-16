# AppDB Backup + PIT Restore — e2e Test Extension Design

**Date:** 2026-07-01
**Branch:** `maciejk/external-appdb`
**File modified:** `docker/mongodb-kubernetes-tests/tests/opsmanager/om_external_appdb.py`
**Marker:** `e2e_om_external_appdb` (unchanged — extends the existing test)

---

## Goal

Prove that once `primary-om-db` has been taken over by a generic `MongoDB` CR (the existing `TestAppDBTakeover` phase), it gains standard backup capabilities that were unavailable while it was OM's internally-managed AppDB — specifically: snapshot backup and point-in-time (PIT) restore, backed by S3 for both the snapshot store and the oplog store.

The PIT assertion is deliberately the *inverse* of the existing `om_ops_manager_backup_restore.py::TestBackupRestorePIT` test: that test restores to a point *before* a post-snapshot change, proving rollback. This test restores to a point *after* a post-snapshot change, proving the oplog store correctly captured and replayed data that only ever existed in the oplog (never in the base snapshot).

---

## Placement

Two new test classes appended to the end of `om_external_appdb.py`, after the existing `TestFinalVerification`:

```
TestSetup
TestPreSwitchCanary
TestSwitchToExternalAppDB
TestPostSwitchVerification
TestAppDBTakeover
TestFinalVerification
TestEnableBackupOnAppDB          <- new
TestBackupSnapshotAndPitRestore  <- new
```

Rationale: backup can only be meaningfully enabled on `primary-om-db` once it's a real `MongoDB` CR under `meta_om` — before the takeover it's OM's internal AppDB, with no user-facing backup config. Placing these classes after `TestFinalVerification` keeps that dependency explicit and reuses all existing fixtures/state.

---

## New fixtures

Reuse existing helpers from `tests.opsmanager.om_ops_manager_backup` (`create_aws_secret`, `create_s3_bucket`) exactly as `om_ops_manager_backup_restore.py` already does — no new S3 plumbing needed.

```python
from tests.opsmanager.om_ops_manager_backup import create_aws_secret, create_s3_bucket

APPDB_S3_SECRET_NAME = "primary-om-db-s3-secret"
APPDB_OPLOG_SECRET_NAME = APPDB_S3_SECRET_NAME + "-oplog"

TEST_DATA = {"_id": "pre-snapshot", "data": "before snapshot"}
POST_SNAPSHOT_DATA = {"_id": "post-snapshot", "data": "after snapshot, oplog-only"}


@fixture(scope="module")
def appdb_s3_bucket(aws_s3_client, namespace: str) -> Iterator[str]:
    create_aws_secret(aws_s3_client, APPDB_S3_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, "test-bucket-appdb-s3")


@fixture(scope="module")
def appdb_oplog_s3_bucket(aws_s3_client, namespace: str) -> Iterator[str]:
    create_aws_secret(aws_s3_client, APPDB_OPLOG_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, "test-bucket-appdb-oplog")


@fixture(scope="module")
def primary_om_external_appdb_collection(primary_om_external_appdb: MongoDB):
    # instantiate per-module (not per-function) since this suite runs its test classes strictly in order
    collection = pymongo.MongoClient(
        primary_om_external_appdb.tester().cnx_string, **primary_om_external_appdb.tester().default_opts
    )["testdb"]
    return collection["testcollection"].with_options(read_preference=ReadPreference.PRIMARY_PREFERRED)
```

Distinct secret/bucket names (`primary-om-db-*` / `-appdb-*`) avoid collisions with the unrelated `om_ops_manager_backup*` test suites, which may run in the same Evergreen task group.

---

## TestEnableBackupOnAppDB

```python
@pytest.mark.e2e_om_external_appdb
class TestEnableBackupOnAppDB:
    def test_enable_backup_on_meta_om(
        self, meta_om: MongoDBOpsManager, appdb_s3_bucket: str, appdb_oplog_s3_bucket: str
    ):
        meta_om.load()
        meta_om["spec"]["backup"]["enabled"] = True
        meta_om["spec"]["backup"]["s3Stores"] = [
            {
                "name": "appdb-s3-store",
                "s3SecretRef": {"name": APPDB_S3_SECRET_NAME},
                "pathStyleAccessEnabled": True,
                "s3BucketEndpoint": "s3.us-east-1.amazonaws.com",
                "s3BucketName": appdb_s3_bucket,
            }
        ]
        meta_om["spec"]["backup"]["s3OpLogStores"] = [
            {
                "name": "appdb-s3-oplog-store",
                "s3SecretRef": {"name": APPDB_OPLOG_SECRET_NAME},
                "pathStyleAccessEnabled": True,
                "s3BucketEndpoint": "s3.us-east-1.amazonaws.com",
                "s3BucketName": appdb_oplog_s3_bucket,
            }
        ]
        meta_om.update()
        meta_om.backup_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_enable_backup_on_primary_om_db(self, primary_om_external_appdb: MongoDB):
        primary_om_external_appdb.load()
        primary_om_external_appdb.configure_backup(mode="enabled")
        primary_om_external_appdb.update()
        primary_om_external_appdb.assert_reaches_phase(Phase.Running, timeout=300)
```

Both S3 stores are set in a single spec update (unlike `om_ops_manager_backup_restore.py`, which deliberately tests the "oplog store missing" validation error as a separate step — not relevant here, so we skip straight to a fully-configured backup spec to keep this compact).

---

## TestBackupSnapshotAndPitRestore

```python
@pytest.mark.e2e_om_external_appdb
class TestBackupSnapshotAndPitRestore:
    def test_add_pre_snapshot_data(self, primary_om_external_appdb_collection):
        primary_om_external_appdb_collection.insert_one(TEST_DATA)

    def test_wait_for_snapshot(self, meta_om: MongoDBOpsManager):
        meta_om.get_om_tester(project_name="appdb-project").wait_until_backup_snapshots_are_ready(expected_count=1)

    def test_add_post_snapshot_data(self, primary_om_external_appdb_collection):
        """This document exists only in the oplog, never in the base snapshot."""
        primary_om_external_appdb_collection.insert_one(POST_SNAPSHOT_DATA)
        time.sleep(30)  # give the PIT window buffer before picking a restore point

    def test_pit_restore(self, meta_om: MongoDBOpsManager):
        pit_millis = time_to_millis(datetime.datetime.now(tz=datetime.timezone.utc) - datetime.timedelta(seconds=15))
        meta_om.get_om_tester(project_name="appdb-project").create_restore_job_pit(pit_millis)

    def test_primary_om_external_appdb_ready_after_restore(self, primary_om_external_appdb: MongoDB):
        time.sleep(5)  # agent needs a moment to act on the restore job, mirrors om_ops_manager_backup_restore.py
        primary_om_external_appdb.assert_reaches_phase(Phase.Running, timeout=300)

    def test_data_survived_restore(self, primary_om_external_appdb_collection):
        """PIT restore targets a point AFTER the post-snapshot insert, so both documents
        must be present — proving the oplog store's data was correctly replayed."""
        records = list(primary_om_external_appdb_collection.find())
        assert TEST_DATA in records, "Pre-snapshot document missing after PIT restore"
        assert POST_SNAPSHOT_DATA in records, "Post-snapshot (oplog-only) document lost during PIT restore"
```

`time_to_millis` is already defined at module scope in `om_external_appdb.py`... actually not — it currently only exists in `om_ops_manager_backup_restore.py`. It will be copied into `om_external_appdb.py` (it's a tiny, self-contained 4-line function; not worth a shared-util extraction for one extra caller).

---

## Non-goals / explicitly out of scope

- No blockstore MDB deployment — S3 is used directly for both snapshot and oplog storage, matching `om_ops_manager_backup_restore.py`'s S3-only pattern (no separate backing MongoDB cluster needed).
- No test of the "oplog store missing" validation error path — not relevant to what's being proven here.
- No restore-from-snapshot-only test (already covered elsewhere, e.g. `TestBackupRestoreFromSnapshot` in `om_ops_manager_backup_restore.py`); this extension is PIT-restore-only, since that's the specific behavior in question.

---

## Verification

- `cd docker/mongodb-kubernetes-tests && python -c "import tests.opsmanager.om_external_appdb; print('OK')"` for a static import/syntax check.
- Full run is manual/local against the user's kind cluster, same as the rest of this file, using `pytest -m e2e_om_external_appdb`.
