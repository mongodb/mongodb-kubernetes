# PIT-based AppDB Disaster Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Convert `TestAppDBDisasterRecovery` to recover a totally-wiped Primary AppDB via point-in-time (PIT) restore through Meta OM, proving oplog replay survives a full PVC wipe.

**Architecture:** Single-file edit to the e2e test `om_appdb_meta_om_mode_switch.py`. The post-snapshot marker change lives only in the backed-up oplog (Meta OM's S3 oplog store), which survives the PVC wipe. The test captures a pre-disaster timestamp, wipes the AppDB, then PIT-restores to that timestamp and asserts the post-snapshot state returns.

**Tech Stack:** Python, pytest, pymongo, kubetester (`OMTester.create_restore_job_pit`, `time_to_millis`).

**Verification note:** This is an e2e test that requires a full kind + Ops Manager environment to actually run, so there is no fast local red/green loop. Local verification is limited to `py_compile` (syntax) and `pytest --collect-only` (imports + names resolve). The real signal comes from running the suite under the `e2e_om_appdb_meta_om_mode_switch` marker in CI/kind.

**Spec:** `docs/superpowers/specs/2026-06-11-appdb-pit-disaster-recovery-design.md`

---

## File Structure

- Modify: `docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_meta_om_mode_switch.py`
  - imports (top of file)
  - constants block (after line 45)
  - `TestAppDBDisasterRecovery` class (lines 283–347)

No new files. No other files touched.

---

## Task 1: Scaffolding — imports, constant, module state

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_meta_om_mode_switch.py`

- [ ] **Step 1: Add `datetime` import**

The file currently starts with `import time` on line 1. Add `datetime` directly after it.

Change:
```python
import time
from typing import Iterator, Optional
```
to:
```python
import datetime
import time
from typing import Iterator, Optional
```

- [ ] **Step 2: Add `time_to_millis` to the omtester import**

Line 9 currently reads:
```python
from kubetester.omtester import OMTester
```
Change it to:
```python
from kubetester.omtester import OMTester, time_to_millis
```

- [ ] **Step 3: Add the changed-marker constant and PIT-target holder**

Line 45 currently reads:
```python
APPDB_TEST_DATA = {"_id": "appdb_pitr_witness", "status": "before_change"}
```
Add immediately after it:
```python
APPDB_TEST_DATA = {"_id": "appdb_pitr_witness", "status": "before_change"}
APPDB_TEST_DATA_CHANGED = {"_id": "appdb_pitr_witness", "status": "after_change"}

# Pre-disaster PIT target (epoch millis), captured before the PVC wipe and read by the
# restore step. Module-level because pytest does not reliably share instance/class state
# across test methods.
_PIT_TARGET: dict[str, float] = {}
```

- [ ] **Step 4: Verify the file still compiles**

Run:
```bash
python -m py_compile docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_meta_om_mode_switch.py && echo OK
```
Expected: `OK` (no syntax errors).

---

## Task 2: Add the change-marker + record-PIT step and update the class docstring

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_meta_om_mode_switch.py:283-295`

- [ ] **Step 1: Update the class docstring to describe PIT recovery**

Lines 283–285 currently read:
```python
@mark.e2e_om_appdb_meta_om_mode_switch
class TestAppDBDisasterRecovery:
    """Simulate complete AppDB data loss (all PVCs deleted) and verify restore from Meta OM backup."""
```
Change the docstring to:
```python
@mark.e2e_om_appdb_meta_om_mode_switch
class TestAppDBDisasterRecovery:
    """Simulate complete AppDB data loss (all PVCs deleted) and verify recovery via
    point-in-time restore from Meta OM backup. The post-snapshot marker change lives only in
    the backed-up oplog, so its return proves oplog replay survives the wipe."""
```

- [ ] **Step 2: Add `test_change_marker_and_record_pit` as the new first step**

Insert this method between the class docstring (ending line 285) and the existing
`test_delete_appdb_pvcs_and_pods` (currently line 287). The new method must run first, so it
must appear before `test_delete_appdb_pvcs_and_pods` in source order.

```python
    def test_change_marker_and_record_pit(self, primary_appdb_collection):
        """Mutate the marker AFTER the snapshot so the new state lives only in the oplog, then
        record a pre-disaster PIT target. Sleep so the backup agent ships oplog slices covering
        that target to Meta OM's S3 oplog store before the cluster is wiped."""
        primary_appdb_collection.update_one(
            {"_id": APPDB_TEST_DATA["_id"]},
            {"$set": {"status": APPDB_TEST_DATA_CHANGED["status"]}},
        )
        # let the write replicate before timestamping
        time.sleep(5)
        _PIT_TARGET["millis"] = time_to_millis(datetime.datetime.now(tz=datetime.timezone.utc))
        # let the backup agent ship oplog slices covering the PIT target to S3 before the wipe
        time.sleep(60)
```

- [ ] **Step 3: Verify the file still compiles**

Run:
```bash
python -m py_compile docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_meta_om_mode_switch.py && echo OK
```
Expected: `OK`.

---

## Task 3: Replace snapshot restore with PIT restore

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_meta_om_mode_switch.py:319-324`

- [ ] **Step 1: Replace `test_restore_from_snapshot` with `test_restore_pit`**

Lines 319–324 currently read:
```python
    def test_restore_from_snapshot(self, meta_om_appdb_tester: OMTester):
        """Restore from the latest snapshot stored in Meta OM.
        PITR is not applicable here: PVC deletion breaks oplog continuity, making any
        pre-disaster pit time invalid. Snapshot restore is the correct recovery mechanism.
        Primary OM goes down during AppDB restore; completion is verified via OM recovery below."""
        meta_om_appdb_tester.create_restore_job_snapshot()
```
Replace that entire method with:
```python
    def test_restore_pit(self, meta_om_appdb_tester: OMTester):
        """Recover via point-in-time restore to the pre-disaster target.
        PIT replays the backed-up oplog stored in Meta OM's S3 oplog store, which survives the
        PVC wipe — so a pre-disaster target remains valid even though the live oplog was lost.
        Primary OM goes down during the AppDB restore; completion is verified via OM recovery below."""
        meta_om_appdb_tester.create_restore_job_pit(_PIT_TARGET["millis"])
```

- [ ] **Step 2: Verify the file still compiles**

Run:
```bash
python -m py_compile docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_meta_om_mode_switch.py && echo OK
```
Expected: `OK`.

---

## Task 4: Assert the post-snapshot (oplog-only) state is restored

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_meta_om_mode_switch.py:332-347`

- [ ] **Step 1: Update `test_data_restored` to expect the changed marker**

The method currently compares against `APPDB_TEST_DATA` (line 341) and references it in the
docstring/error. Update it to expect `APPDB_TEST_DATA_CHANGED`, since the PIT target was
recorded after the `after_change` mutation.

Replace lines 332–347:
```python
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
```
with:
```python
    def test_data_restored(self, primary_appdb_collection):
        """Wait until the PIT-restored marker appears in its post-snapshot ("after_change") state.
        That state only ever existed in the oplog, so seeing it return proves oplog replay
        survived the wipe. Retries on connection errors (mongod restarting during apply) and on
        the not-yet-restored state."""
        start = time.time()
        timeout = 3600
        last_error = None
        while time.time() - start < timeout:
            try:
                records = list(primary_appdb_collection.find({"_id": APPDB_TEST_DATA["_id"]}))
                if records == [APPDB_TEST_DATA_CHANGED]:
                    return
                last_error = f"data not yet at expected PIT state: {records}"
            except Exception as e:
                last_error = e
            time.sleep(5)
        raise AssertionError(f"Data not PIT-restored within {timeout}s. Last error: {last_error}")
```

- [ ] **Step 2: Verify the file still compiles**

Run:
```bash
python -m py_compile docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_meta_om_mode_switch.py && echo OK
```
Expected: `OK`.

---

## Task 5: Verify collection + commit

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_meta_om_mode_switch.py`

- [ ] **Step 1: Verify pytest can collect the suite (imports + names resolve)**

Run from the tests package root:
```bash
cd docker/mongodb-kubernetes-tests && python -m pytest tests/opsmanager/om_appdb_meta_om_mode_switch.py --collect-only -q
```
Expected: collection succeeds with no import/NameError. The collected items should include
`TestAppDBDisasterRecovery::test_change_marker_and_record_pit`,
`...::test_restore_pit`, and `...::test_data_restored` (and no
`test_restore_from_snapshot`).

If collection fails because test dependencies are not installed in the active interpreter, fall
back to the `py_compile` check from earlier tasks and note that collection must be run in the
test venv.

- [ ] **Step 2: Confirm the snapshot-restore method is fully gone**

Run:
```bash
grep -n "create_restore_job_snapshot\|test_restore_from_snapshot" docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_meta_om_mode_switch.py || echo "NONE - good"
```
Expected: `NONE - good`.

- [ ] **Step 3: Confirm the new wiring is present**

Run:
```bash
grep -n "APPDB_TEST_DATA_CHANGED\|_PIT_TARGET\|create_restore_job_pit\|test_change_marker_and_record_pit\|time_to_millis" docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_meta_om_mode_switch.py
```
Expected: matches for the import, the constant/holder definitions, the new record-PIT method,
and the PIT restore call.

- [ ] **Step 4: Commit**

```bash
git add docker/mongodb-kubernetes-tests/tests/opsmanager/om_appdb_meta_om_mode_switch.py
git commit -m "test: recover AppDB via PIT restore in disaster-recovery e2e"
```

---

## Notes / out of scope

- **Not run locally:** the actual e2e (kind + Ops Manager) is run in CI or a kind dev loop, not in this plan. The decisive assertion is `test_data_restored` seeing `status: "after_change"`.
- **Tuning risk:** the 60s oplog-ship sleep in Task 2 may need adjustment if the test OM ships oplog slices less frequently; symptom would be `create_restore_job_pit` failing with an invalid restore point (it retries internally up to its `timeout_seconds`).
- **Hypothesis under test:** if OM invalidates pre-disaster restore points after the wiped cluster re-registers, `test_restore_pit` / `test_data_restored` will fail — meaning the original snapshot-based approach was correct and we revert.
- **Left untouched (per scoping):** duplicate `kubetester` imports (lines 4/6) and the unused `assert_data_got_restored` import (line 13). A changelog entry is not included — this is a test-only change.
