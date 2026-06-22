"""Shared promote-and-prune loop for VM migration E2E (non-TLS and TLS/X509).

Mirrors the flow in ``vm_migration.test_promote_and_prune``: for each VM replica, scale up
``spec.members`` by one with the new member pinned to priority/votes 0, prune one
``externalMembers`` entry, then restore full priority/votes for that in-cluster member.

After extend: waits for ``Migrating`` reason ``Extending`` then ``status.phase == Running``.
After prune (non-final): checks ``Running`` + ``Pruning`` in a single poll to avoid the race
where sequential checks miss the transient ``Pruning`` state before it flips to ``InProgress``.
After re-prioritize (non-final): checks ``Running`` + ``InProgress`` in one poll.
"""

from __future__ import annotations

from typing import Tuple

from kubetester import try_load
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.mongotester import MongoTester
from kubetester.omtester import OMTester
from kubetester.phase import Phase
from tests import test_logger
from tests.conftest import local_operator
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


def _connection_string(mdb_migration: MongoDB) -> Tuple[str, str]:
    """Read connectionString.standard and connectionString.standardSrv from the credential-less secret published by the operator."""
    secret = KubernetesTester.read_secret(mdb_migration.namespace, f"{mdb_migration.name}-connection-string")
    return secret.get("connectionString.standard", ""), secret.get("connectionString.standardSrv", "")


def _k8s_hostnames(mdb_migration: MongoDB) -> list:
    """Return the expected k8s pod DNS hostnames (host:port) for all RS members."""
    svc = f"{mdb_migration.name}-svc"
    return [
        f"{mdb_migration.name}-{i}.{svc}.{mdb_migration.namespace}.svc.cluster.local:27017"
        for i in range(mdb_migration.get_members())
    ]


def promote_and_prune_members(mdb: MongoDB, vm_sts: dict, om_tester: OMTester, test_connection: bool = False) -> None:
    """Run the full VM → K8s cutover: extend, prune, and re-prioritize one member at a time.

    After extend: ``Migrating`` ``Extending`` then ``Running``. After prune (non-final):
    ``Running`` + ``Pruning`` in one poll. After re-prioritize (non-final): ``Running`` +
    ``InProgress`` in one poll.
    """
    try_load(mdb)
    spec = mdb["spec"]
    if not isinstance(spec.get("memberConfig"), list):
        spec["memberConfig"] = []

    # Using length of external members to allow reruns
    total_vms = len(mdb["spec"]["externalMembers"])

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
        pruned = mdb["spec"]["externalMembers"].pop()
        mdb.update()
        is_last_prune = i == total_vms - 1
        if not is_last_prune:
            # Check Running + Pruning atomically in one poll. Pruning is a single-reconcile
            # transient state — sequential checks race with the next reconcile flipping it
            # to InProgress before we observe it.
            wait_until_phase_and_migrating_condition_reason(
                mdb, PHASE_RUNNING, MIGRATING_CONDITION_REASON_PRUNING, timeout=600
            )
        else:
            wait_until_running_and_migration_complete(mdb)

        # --- Re-prioritize: restore full votes/priority ---
        logger.info(f"Restoring full priority/votes for member {i + 1} of {total_vms}")
        mdb["spec"]["memberConfig"][i] = {"priority": "1", "votes": 1}
        mdb.update()
        if not is_last_prune:
            wait_until_phase_and_migrating_condition_reason(
                mdb, PHASE_RUNNING, MIGRATING_CONDITION_REASON_IN_PROGRESS, timeout=600
            )
        else:
            mdb.assert_reaches_phase(phase=Phase.Running)

        # After prune: removed VM hostname must be gone; surviving hosts must be present.
        conn_str, conn_srv = _connection_string(mdb)
        assert (
            pruned["hostname"] not in conn_str
        ), f"pruned hostname {pruned['hostname']!r} still in connection string after prune {i}"
        for hostname in _k8s_hostnames(mdb):
            assert hostname in conn_str, f"k8s hostname {hostname!r} missing after prune {i}"
        for em in mdb["spec"]["externalMembers"]:
            assert em["hostname"] in conn_str, f"external member {em['hostname']!r} missing after prune {i}"

        if test_connection:
            # Test connectivity with generated connection string
            MongoTester(conn_str).assert_connectivity(attempts=1)
            if not local_operator():
                # srv connections don't work via kubefwd.
                # mongodb+srv:// defaults to tls=true in the driver, so a non-TLS deployment
                # must explicitly disable it (the operator only adds ssl=true for TLS deployments, but does not ssl=false).
                MongoTester(conn_srv, use_ssl=mdb.is_tls_enabled()).assert_connectivity(attempts=1)

        om_tester.assert_cluster_available(f"{vm_sts['metadata']['name']}-rs")
        ac_tester = om_tester.get_automation_config_tester()
        total_members = mdb.get_members() + len(mdb["spec"]["externalMembers"])
        assert len(ac_tester.get_all_processes()) == total_members
        assert len(ac_tester.get_monitoring_versions()) == total_members
        assert len(ac_tester.get_backup_versions()) == total_members
        assert len(ac_tester.get_replica_set_processes(f"{vm_sts['metadata']['name']}-rs")) == total_members


def _wait_migrating_lifecycle_reason_then_running(mdb: MongoDB, migrating_reason: str) -> None:
    """Wait for Migrating=migrating_reason (Extending or Pruning), then status.phase Running."""
    wait_until_migrating_condition_reason(mdb, migrating_reason, timeout=600)
    mdb.assert_reaches_phase(Phase.Running, timeout=600)
