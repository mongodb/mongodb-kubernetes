# Convert AppDB disaster-recovery e2e test to PIT restore

**Date:** 2026-06-11
**Test file:** `docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_meta_om_mode_switch.py`
**Marker:** `e2e_om_appdb_meta_om_mode_switch`

## Goal

Convert the existing `TestAppDBDisasterRecovery` class to recover a totally-lost Primary
AppDB via **point-in-time (PIT) restore** instead of snapshot restore, and prove that PIT
replay genuinely works after a complete PVC wipe.

## Background / architecture

This suite exercises the AppDB headless -> online mode switch. A "Primary OM" has its AppDB
managed by a separate "Meta OM" instance. Meta OM owns backup, configured with S3 snapshot
stores (`spec.backup.s3Stores`) and S3 oplog stores (`spec.backup.s3OpLogStores`).

For the Primary AppDB:

| What | Where it is stored | Configured on |
|------|--------------------|---------------|
| Snapshot **data** | S3 snapshot store | Meta OM |
| Oplog **data** (for PIT) | S3 oplog store | Meta OM |
| Backup **metadata/catalog** (snapshots, restore points, jobs) | Meta OM's own AppDB | Meta OM |

The backup daemon's local `HeadDB` (`/head/` PVC) is a live working replica used to cut
snapshots; it is not the catalog.

Because both the snapshot data and the oplog slices live in Meta OM's S3 stores, they survive
a total wipe of the Primary AppDB PVCs. This is the architectural basis for the DR claim: even
total loss of the Primary AppDB is recoverable from Meta OM.

## The problem this design solves

The existing `TestAppDBDisasterRecovery` recovers via `create_restore_job_snapshot()` and
carries an explicit comment claiming:

> "PITR is not applicable here: PVC deletion breaks oplog continuity, making any pre-disaster
> pit time invalid. Snapshot restore is the correct recovery mechanism."

This claim is questionable: PIT restore replays the **backed-up** oplog stored in Meta OM's S3
oplog store, not the live oplog. The live oplog continuity break is irrelevant. Whether OM
honors a pre-disaster PIT target after the cluster is wiped and re-registers is the genuine
uncertainty this test will resolve.

A PIT restore also needs a **target timestamp**. The canonical PIT test (`om_ops_manager_backup_restore.py::TestBackupRestorePIT`)
uses `now - 15s`, but here "now" at restore time is *after* the disaster, when no valid data or
oplog exists. The target must therefore be a **pre-disaster timestamp** captured while backup
was still healthy.

## Design

Variant chosen: **oplog-proving**. The post-snapshot state lives only in the oplog, so a
successful restore proves oplog replay (not merely snapshot recovery) survives the wipe.

### New constant

```python
APPDB_TEST_DATA_CHANGED = {"_id": "appdb_pitr_witness", "status": "after_change"}
```

The existing `APPDB_TEST_DATA = {"_id": "appdb_pitr_witness", "status": "before_change"}` is
inserted before backup is enabled, so it is captured in the first snapshot.

### Module state

A module-level holder for the captured PIT target (e.g. `_pit_target_millis = {}`), written by
the first DR step and read by the restore step. Module-level rather than instance/class state
because pytest does not reliably share instance state across test methods.

### Imports

- add `import datetime`
- add `from kubetester.omtester import time_to_millis` (module-level helper at
  `kubetester/omtester.py:915`; falls back to a local helper if needed)
- `import time` already present

### `TestAppDBDisasterRecovery` steps (in order)

1. **`test_change_marker_and_record_pit`** *(new first step)*
   - Update the marker to `APPDB_TEST_DATA_CHANGED` (`before_change` -> `after_change`). This
     new state exists only in the oplog, not in the snapshot.
   - Brief settle so the write replicates.
   - Record `pit_target_millis = time_to_millis(datetime.datetime.now(tz=datetime.timezone.utc))`
     into module state.
   - `sleep(~60s)` so the backup agent ships oplog slices **covering** `pit_target` to Meta OM's
     S3 oplog store before the cluster is wiped. (This sleep is the primary flakiness/tuning
     risk — it depends on oplog slice frequency in the test OM configuration.)

2. **`test_delete_appdb_pvcs_and_pods`** — unchanged. Delete each `data-<appdb-sts>-<i>` PVC then
   the corresponding pod; the StatefulSet recreates pods with fresh empty PVCs.

3. **`test_appdb_reaches_running_after_recreation`** — unchanged. Operator recreates AppDB; agent
   reconnects to Meta OM.

4. **`test_data_is_gone_after_recreation`** — unchanged. Fresh PVCs => marker absent (retries
   while pods start).

5. **`test_restore_pit`** *(replaces `test_restore_from_snapshot`)*
   - `meta_om_appdb_tester.create_restore_job_pit(pit_target_millis)`
   - Rewrite the docstring: PIT replays the S3-resident backed-up oplog, which survives PVC loss;
     remove the "PITR not applicable" claim.

6. **`test_primary_om_reaches_running_after_restore`** — unchanged. OM recovery implies the AppDB
   was fully restored.

7. **`test_data_restored`** — assert the marker equals `APPDB_TEST_DATA_CHANGED`. This is the key
   assertion: the `after_change` state only ever existed in the oplog, so its return proves oplog
   replay worked after the wipe.

### Data flow at restore time

- Restore is driven through Meta OM (`meta_om_appdb_tester`, the OMTester for the `primary-appdb`
  project in Meta OM); the restore job is written to Meta OM's catalog (its AppDB).
- Snapshot + oplog data are pulled from Meta OM's S3 stores (`meta-om-s3`, `meta-om-oplog`).
- The agent on the recreated Primary AppDB pods executes the restore, rewriting the AppDB — which
  is why Primary OM must re-stabilize afterward.

## Verification

This is an e2e test; verification is the test run itself under the
`e2e_om_appdb_meta_om_mode_switch` marker. No unit layer.

The decisive signal: step 7 sees `status: "after_change"` return after a full PVC wipe.

## Risks / open questions

- **Oplog ship timing.** If the backup agent has not shipped oplog covering `pit_target` to S3
  before the wipe, the restore point is unavailable. The ~60s sleep is a tuning point; the
  canonical PIT test ships within ~30s, suggesting test OM config ships oplog quickly.
- **Cluster re-registration after wipe.** If OM invalidates pre-disaster restore points once the
  wiped cluster re-registers (new state / fresh replica set), PIT to a pre-disaster target may
  fail. This is precisely the hypothesis under test and may turn out to be true — in which case
  the existing snapshot-based comment was correct and we revert.

## Out of scope (per scoping decisions)

- A standalone in-place PIT-rewind test (insert -> snapshot -> change -> PIT-rewind in place).
- Cleanup of the concurrent-edit issues: duplicate `kubetester` imports (lines 4 and 6) and the
  unused `assert_data_got_restored` import (line 13). Note `assert_data_got_restored` remains
  imported-but-unused after this change.
