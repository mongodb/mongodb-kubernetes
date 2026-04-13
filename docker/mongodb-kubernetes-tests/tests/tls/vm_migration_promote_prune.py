"""Shared promote-and-prune loop for VM migration E2E (non-TLS and TLS/X509).

Mirrors the flow in ``vm_migration.test_promote_and_prune``: for each VM replica, scale up
``spec.members`` by one with the new member pinned to priority/votes 0, prune one
``externalMembers`` entry, then restore full priority/votes for that in-cluster member.

After every spec change the helper polls for Running **and** the expected migration state
in a single check, avoiding a race where a second reconcile flips an ephemeral phase
before we observe it:

    Extend  → Running + Extending  (new k8s member, running count increased)
    Prune   → Running + Pruning    … except on last prune → Running + migration absent
    Reprio  → Running + InProgress (stable counts, nothing actively changing)
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
    wait_until_phase_and_migration_phase,
    wait_until_running_and_migration_absent,
)

logger = test_logger.get_test_logger(__name__)

PHASE_RUNNING = "Running"


def promote_and_prune_members(mdb: MongoDB, vm_sts: dict) -> None:
    """Run the full VM → K8s cutover: extend, prune, and re-prioritize one member at a time.

    After every spec change, polls for ``status.phase == Running`` and the expected
    ``status.migration`` state.
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
        wait_until_phase_and_migration_phase(mdb, PHASE_RUNNING, MIGRATION_PHASE_EXTENDING)

        # --- Prune: remove one VM member ---
        logger.info(f"Removing one VM member {i + 1} of {total_vms}")
        mdb["spec"]["externalMembers"].pop()
        mdb.update()
        is_last_prune = i == total_vms - 1
        if is_last_prune:
            wait_until_running_and_migration_absent(mdb)
        else:
            wait_until_phase_and_migration_phase(mdb, PHASE_RUNNING, MIGRATION_PHASE_PRUNING)

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
