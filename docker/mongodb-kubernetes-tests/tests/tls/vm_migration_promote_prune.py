"""Shared promote-and-prune loop for VM migration E2E (non-TLS and TLS/X509).

Mirrors the flow in ``vm_migration.test_promote_and_prune``: for each VM replica, scale up
``spec.members`` by one with the new member pinned to priority/votes 0, prune one
``externalMembers`` entry, then restore full priority/votes for that in-cluster member.

After extend (resp. each non-final prune), the helper first waits for ``Migrating`` reason
``Extending`` (resp. ``Pruning``), then for ``status.phase == Running``. After re-prioritize
(non-final), it waits for ``Running`` and ``Migrating`` reason ``InProgress`` in one poll.
``Running`` follows successful Ops Manager reconciliation on that reconcile path, which avoids
the next spec change racing an in-flight membership update (OM 400).
"""

from __future__ import annotations

from kubetester import try_load
from kubetester.mongodb import MongoDB
from kubetester.phase import Phase
from tests import test_logger
from tests.tls.vm_migration_dry_run import (
    MIGRATING_CONDITION_REASON_EXTENDING,
    MIGRATING_CONDITION_REASON_IN_PROGRESS,
    MIGRATING_CONDITION_REASON_PRUNING,
    wait_until_migrating_condition_reason,
    wait_until_phase_and_migrating_condition_reason,
    wait_until_running_and_migration_complete,
)

logger = test_logger.get_test_logger(__name__)

PHASE_RUNNING = "Running"


def promote_and_prune_members(mdb: MongoDB, vm_sts: dict) -> None:
    """Run the full VM → K8s cutover: extend, prune, and re-prioritize one member at a time.

    After extend: ``Migrating`` ``Extending`` then ``Running``. After each non-final prune:
    ``Migrating`` ``Pruning`` then ``Running``. After re-prioritize (non-final): ``Running`` and
    ``Migrating`` ``InProgress`` in one poll.
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
        _wait_migrating_lifecycle_reason_then_running(mdb, MIGRATING_CONDITION_REASON_EXTENDING)

        # --- Prune: remove one VM member ---
        logger.info(f"Removing one VM member {i + 1} of {total_vms}")
        mdb["spec"]["externalMembers"].pop()
        mdb.update()
        is_last_prune = i == total_vms - 1
        if not is_last_prune:
            _wait_migrating_lifecycle_reason_then_running(mdb, MIGRATING_CONDITION_REASON_PRUNING)

        # --- Re-prioritize: restore full votes/priority ---
        logger.info(f"Restoring full priority/votes for member {i + 1} of {total_vms}")
        mdb["spec"]["memberConfig"][i] = {"priority": "1", "votes": 1}
        mdb.update()
        if not is_last_prune:
            wait_until_phase_and_migrating_condition_reason(
                mdb, PHASE_RUNNING, MIGRATING_CONDITION_REASON_IN_PROGRESS, timeout=600
            )

        wait_until_running_and_migration_complete(mdb)


def _wait_migrating_lifecycle_reason_then_running(mdb: MongoDB, migrating_reason: str) -> None:
    """Wait for Migrating=migrating_reason (Extending or Pruning), then status.phase Running."""
    wait_until_migrating_condition_reason(mdb, migrating_reason, timeout=600)
    mdb.assert_reaches_phase(Phase.Running, timeout=600)
