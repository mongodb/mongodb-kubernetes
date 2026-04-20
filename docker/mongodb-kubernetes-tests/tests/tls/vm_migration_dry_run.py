"""Shared VM migration dry-run (connectivity-only) flow for plain-TLS and X509 E2E tests.

The operator records migration lifecycle on ``status.conditions`` (``Migrating``,
``NetworkConnectivityVerification``) and persists the last observed external-member count on
``status.migrationObservedExternalMembersCount``.
"""

from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB

# Annotation that triggers migration dry-run (connectivity validation only, no OM/StatefulSet changes).
MIGRATION_DRY_RUN_ANNOTATION = "mongodb.com/migration-dry-run"
CONDITION_NETWORK_CONNECTIVITY_VERIFIED = "NetworkConnectivityVerification"
# Top-level condition indicating whether VM-to-K8s migration is active.
CONDITION_MIGRATING = "Migrating"
# Literal status.conditions[Migrating].reason values while Migrating=True (api/v1/status MigratingReason*).
MIGRATING_CONDITION_REASON_VALIDATING = "Validating"
MIGRATING_CONDITION_REASON_EXTENDING = "Extending"
MIGRATING_CONDITION_REASON_PRUNING = "Pruning"
MIGRATING_CONDITION_REASON_IN_PROGRESS = "InProgress"


def _migration_observed_external_count(s: dict) -> int:
    """Last reconciled externalMembers count from status (0 if unset / null / omitempty)."""
    v = s.get("migrationObservedExternalMembersCount")
    if v is None:
        return 0
    return int(v) if isinstance(v, int) else 0


def _status_dict(mdb: MongoDB) -> dict:
    try:
        s = mdb["status"]
    except (KeyError, TypeError):
        return {}
    return s if isinstance(s, dict) else {}


def _network_connectivity_true_in_conditions(conditions) -> bool:
    if not conditions:
        return False
    for c in conditions:
        if c.get("type") == CONDITION_NETWORK_CONNECTIVITY_VERIFIED and c.get("status") == "True":
            return True
    return False


def _get_condition(conditions, condition_type: str) -> dict | None:
    """Find a condition by type in a list of conditions."""
    if not conditions:
        return None
    for c in conditions:
        if c.get("type") == condition_type:
            return c
    return None



def _migration_connectivity_passed(mdb: MongoDB) -> bool:
    mdb.load()
    conditions = _status_dict(mdb).get("conditions", [])
    for c in conditions:
        if c.get("type") == CONDITION_NETWORK_CONNECTIVITY_VERIFIED:
            if c.get("status") == "True":
                return True
            if c.get("status") == "False":
                raise AssertionError(f"connectivity check failed: {c.get('reason')}: {c.get('message')}")
    return False


def _assert_migration_status_after_dry_run_pass(mdb: MongoDB) -> None:
    """After connectivity passes, the operator should have written migration conditions on status."""
    mdb.load()
    status = _status_dict(mdb)
    conditions = status.get("conditions", [])
    mig = _get_condition(conditions, CONDITION_MIGRATING)
    assert mig is not None, "expected Migrating condition while migration-dry-run annotation is set"
    assert mig.get("status") == "True", f"expected Migrating=True during dry-run, got {mig!r}"
    assert mig.get("reason") == MIGRATING_CONDITION_REASON_VALIDATING, (
        f"expected Migrating.reason {MIGRATING_CONDITION_REASON_VALIDATING!r} while dry-run annotation is set, "
        f"got {mig.get('reason')!r}"
    )
    assert _network_connectivity_true_in_conditions(
        conditions
    ), "expected NetworkConnectivityVerification status True on status.conditions"
    assert "migration" not in status or status.get("migration") in (
        None,
        {},
    ), "status.migration must not be set; migration state lives under status.conditions"


def run_migration_dry_run_connectivity_passes(mdb: MongoDB, *, timeout: int = 600) -> None:
    """Dry-run annotation → wait for connectivity → assert ``Validating`` → clear annotation.

    While the annotation is present, ``Migrating`` is True with ``reason: Validating``; connectivity
    progress is in ``NetworkConnectivityVerification`` on ``status.conditions``. After the annotation
    is removed, the operator transitions to ``InProgress`` on the next reconcile.
    """
    mdb.load()
    if "metadata" not in mdb:
        mdb["metadata"] = {}
    if "annotations" not in mdb["metadata"]:
        mdb["metadata"]["annotations"] = {}
    mdb["metadata"]["annotations"][MIGRATION_DRY_RUN_ANNOTATION] = "true"
    mdb.update()

    mdb.wait_for(_migration_connectivity_passed, timeout=timeout)

    _assert_migration_status_after_dry_run_pass(mdb)

    mdb.load()
    ann = mdb.backing_obj.get("metadata").get("annotations")
    if ann is not None and MIGRATION_DRY_RUN_ANNOTATION in ann:
        ann[MIGRATION_DRY_RUN_ANNOTATION] = None
        mdb.update()
    wait_until_migrating_condition_reason(mdb, MIGRATING_CONDITION_REASON_IN_PROGRESS)


def assert_migrating_condition_reason(mdb: MongoDB, expected_reason: str) -> None:
    """Assert Migrating=True and Migrating.reason equals expected_reason."""
    mdb.load()
    conditions = _status_dict(mdb).get("conditions", [])
    mig = _get_condition(conditions, CONDITION_MIGRATING)
    assert mig is not None, f"expected Migrating condition with reason {expected_reason!r}, got {conditions!r}"
    assert mig.get("status") == "True", f"expected Migrating=True for reason {expected_reason!r}, got {mig!r}"
    actual = mig.get("reason")
    assert actual == expected_reason, f"expected Migrating.reason={expected_reason!r}, got {actual!r}"


def wait_until_migrating_condition_reason(mdb: MongoDB, expected_reason: str, *, timeout: int = 120) -> None:
    """Poll until Migrating=True and Migrating.reason matches expected_reason."""

    def _ok() -> bool:
        mdb.load()
        s = _status_dict(mdb)
        conditions = s.get("conditions", [])
        mig = _get_condition(conditions, CONDITION_MIGRATING)
        if mig is None or mig.get("status") != "True":
            return False
        return mig.get("reason") == expected_reason

    KubernetesTester.wait_until(_ok, timeout=timeout)


def wait_until_phase_and_migrating_condition_reason(
    mdb: MongoDB, phase: str, migrating_reason: str, *, timeout: int = 600
) -> None:
    """Poll until status.phase, Migrating=True, and Migrating.reason all match.

    Migrating reasons (Extending, Pruning, …) are ephemeral — they're recomputed on every
    reconcile and can flip on the very next one if counts stabilize. Checking
    status.phase and Migrating.reason in a single poll avoids the race where a second
    reconcile runs between two separate assertions.
    """

    def _ok() -> bool:
        mdb.load()
        s = _status_dict(mdb)
        if s.get("phase") != phase:
            return False
        conditions = s.get("conditions", [])
        mig = _get_condition(conditions, CONDITION_MIGRATING)
        if mig is None or mig.get("status") != "True":
            return False
        return mig.get("reason") == migrating_reason

    KubernetesTester.wait_until(_ok, timeout=timeout)


def wait_until_running_and_migration_in_progress(mdb: MongoDB, *, timeout: int = 600) -> None:
    """Poll until status.phase is Running and Migrating.reason is InProgress.

    Used after operations where counts stabilize — externalMembers still exist but nothing
    is actively extending or pruning.
    """

    def _ok() -> bool:
        mdb.load()
        s = _status_dict(mdb)
        if s.get("phase") != "Running":
            return False
        conditions = s.get("conditions", [])
        mig = _get_condition(conditions, CONDITION_MIGRATING)
        return (
            mig is not None
            and mig.get("status") == "True"
            and mig.get("reason") == MIGRATING_CONDITION_REASON_IN_PROGRESS
        )

    KubernetesTester.wait_until(_ok, timeout=timeout)


def wait_until_running_and_migration_absent(mdb: MongoDB, *, timeout: int = 600) -> None:
    """Poll until status.phase is Running, migration helper conditions are cleared, and Migrating=False.

    When all externalMembers are removed the operator unsets ``migrationObservedExternalMembersCount``,
    removes ``NetworkConnectivityVerification``, and sets ``Migrating`` to False.
    """

    def _ok() -> bool:
        mdb.load()
        s = _status_dict(mdb)
        if s.get("phase") != "Running":
            return False
        if _migration_observed_external_count(s) != 0:
            return False
        conditions = s.get("conditions", [])
        cond = _get_condition(conditions, CONDITION_MIGRATING)
        return cond is not None and cond.get("status") == "False"

    KubernetesTester.wait_until(_ok, timeout=timeout)


def assert_migration_absent(mdb: MongoDB, timeout: int = 120) -> None:
    """Wait for migration-complete status: migrationObservedExternalMembersCount absent, Migrating=False."""

    def _migration_complete() -> bool:
        mdb.load()
        s = _status_dict(mdb)
        if _migration_observed_external_count(s) != 0:
            return False
        conditions = s.get("conditions", [])
        cond = _get_condition(conditions, CONDITION_MIGRATING)
        return cond is not None and cond.get("status") == "False"

    KubernetesTester.wait_until(_migration_complete, timeout=timeout)
