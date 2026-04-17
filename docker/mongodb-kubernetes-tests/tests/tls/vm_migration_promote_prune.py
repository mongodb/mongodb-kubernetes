"""Shared promote-and-prune loop for VM migration E2E (non-TLS and TLS/X509).

Mirrors the flow in ``vm_migration.test_promote_and_prune``: for each VM replica, scale up
``spec.members`` by one with the new member pinned to priority/votes 0, prune one
``externalMembers`` entry, then restore full priority/votes for that in-cluster member.

After every spec change the helper polls for the expected migration state:

    Extend  → Extending            (StatefulSet may be Pending while extending proceeds)
    Prune   → Pruning              (StatefulSet may be Pending while pruning proceeds)
    Reprio  → Running + InProgress (stable counts, nothing actively changing)

Note: During Extend and Prune we only wait for migration.phase, not status.phase. The
StatefulSet may still be Pending (e.g., agent reconciling X509 certs) while the operation
completes in Ops Manager. Requiring Running + Extending/Pruning simultaneously is a race.
"""

from __future__ import annotations

from kubetester import try_load
from kubetester.mongodb import MongoDB
from tests import test_logger
from tests.tls.vm_migration_dry_run import (
    MIGRATION_PHASE_EXTENDING,
    MIGRATION_PHASE_IN_PROGRESS,
    MIGRATION_PHASE_PRUNING,
    assert_migration_absent,
    wait_until_migration_phase,
    wait_until_phase_and_migration_phase,
    wait_until_running_and_migration_absent,
)

logger = test_logger.get_test_logger(__name__)

PHASE_RUNNING = "Running"


def promote_and_prune_members(mdb: MongoDB, vm_sts: dict) -> None:
    """Run the full VM → K8s cutover: extend, prune, and re-prioritize one member at a time.

    After every spec change, polls for the expected ``status.migration`` state.
    For Extend and Prune, we only wait for migration.phase (not status.phase) to avoid
    a race where the StatefulSet remains Pending while the operation completes.
    """
    try_load(mdb)
    spec = mdb["spec"]
    if not isinstance(spec.get("memberConfig"), list):
        spec["memberConfig"] = []

    total_vms = vm_sts["spec"]["replicas"]

    for i in range(total_vms):
        # --- Extend: add one k8s member with votes/priority 0 ---
        logger.info(f"Adding one member {i + 1} of {total_vms}")
        mdb["spec"]["members"] = i + 1
        if len(mdb["spec"]["memberConfig"]) <= i:
            mdb["spec"]["memberConfig"].append({"priority": "0", "votes": 0})
        else:
            mdb["spec"]["memberConfig"][i] = {"priority": "0", "votes": 0}
        mdb.update()
        # Don't require Running - StatefulSet may still be Pending while extending proceeds
        wait_until_migration_phase(mdb, MIGRATION_PHASE_EXTENDING)

        # --- Prune: remove one VM member ---
        logger.info(f"Removing one VM member {i + 1} of {total_vms}")
        mdb["spec"]["externalMembers"].pop()
        mdb.update()
        is_last_prune = i == total_vms - 1
        if is_last_prune:
            wait_until_running_and_migration_absent(mdb)
        else:
            # Don't require Running - StatefulSet may still be Pending while pruning proceeds
            wait_until_migration_phase(mdb, MIGRATION_PHASE_PRUNING)

        # --- Re-prioritize: restore full votes/priority ---
        logger.info(f"Restoring full priority/votes for member {i + 1} of {total_vms}")
        mdb["spec"]["memberConfig"][i] = {"priority": "1", "votes": 1}
        mdb.update()
        if is_last_prune:
            # After the last prune, migration is complete and absent from status
            wait_until_running_and_migration_absent(mdb)
        else:
            wait_until_phase_and_migration_phase(mdb, PHASE_RUNNING, MIGRATION_PHASE_IN_PROGRESS)

    assert_migration_absent(mdb)
