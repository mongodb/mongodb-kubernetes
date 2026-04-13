"""Shared VM migration dry-run (connectivity-only) flow for plain-TLS and X509 E2E tests.

The operator records connectivity results on ``status.migration`` (lifecycle phase, conditions,
observed external member count).
"""

from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB

# Annotation that triggers migration dry-run (connectivity validation only, no OM/StatefulSet changes).
MIGRATION_DRY_RUN_ANNOTATION = "mongodb.com/migration-dry-run"
CONDITION_NETWORK_CONNECTIVITY_VERIFIED = "NetworkConnectivityVerification"
# Matches api/v1/status lifecycle phase constants.
MIGRATION_PHASE_VALIDATING = "Validating"
MIGRATION_PHASE_EXTENDING = "Extending"
MIGRATION_PHASE_PRUNING = "Pruning"
MIGRATION_PHASE_IN_PROGRESS = "InProgress"


def _spec_external_members_count(mdb: MongoDB) -> int:
    try:
        ext = mdb["spec"]["externalMembers"]
    except (KeyError, TypeError):
        return 0
    return len(ext) if isinstance(ext, list) else 0


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


def _migration_connectivity_passed(mdb: MongoDB) -> bool:
    mdb.load()
    migration = _status_dict(mdb).get("migration")
    if not isinstance(migration, dict):
        return False
    for c in migration.get("conditions", []):
        if c.get("type") == CONDITION_NETWORK_CONNECTIVITY_VERIFIED:
            if c.get("status") == "True":
                return True
            if c.get("status") == "False":
                raise AssertionError(f"connectivity check failed: {c.get('reason')}: {c.get('message')}")
    return False


def _assert_migration_status_after_dry_run_pass(mdb: MongoDB) -> None:
    """After connectivity passes, the operator should have written status.migration"""
    mdb.load()
    status = _status_dict(mdb)
    migration = status.get("migration")
    assert migration is not None, (
        "expected status.migration with connectivity dry-run result; "
        "operator should populate migration subresource while migration-dry-run annotation is set"
    )
    assert isinstance(migration, dict), f"status.migration should be an object, got {type(migration)!r}"
    assert migration.get("phase") == MIGRATION_PHASE_VALIDATING, (
        f"expected migration.phase {MIGRATION_PHASE_VALIDATING!r} while dry-run annotation is set, "
        f"got {migration.get('phase')!r}"
    )
    assert _network_connectivity_true_in_conditions(
        migration.get("conditions")
    ), "expected NetworkConnectivityVerification status True on status.migration.conditions"


def run_migration_dry_run_connectivity_passes(mdb: MongoDB, *, timeout: int = 600) -> None:
    """Dry-run annotation → wait for connectivity → assert ``Validating`` phase → clear annotation.

    While the annotation is present, ``status.migration.phase`` is ``Validating``; connectivity
    progress is in ``status.migration.conditions``. After the annotation is removed, the operator
    transitions to ``InProgress`` on the next reconcile.
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
    wait_until_migration_phase(mdb, MIGRATION_PHASE_IN_PROGRESS)


def assert_migration_phase(mdb: MongoDB, expected_phase: str) -> None:
    """Assert status.migration.phase equals expected_phase."""
    mdb.load()
    migration = _status_dict(mdb).get("migration")
    assert isinstance(
        migration, dict
    ), f"expected status.migration to be present with phase {expected_phase!r}, got {migration!r}"
    actual = migration.get("phase")
    assert actual == expected_phase, f"expected status.migration.phase={expected_phase!r}, got {actual!r}"


def wait_until_migration_phase(mdb: MongoDB, expected_phase: str, *, timeout: int = 120) -> None:
    """Poll until status.migration.phase matches (reconcile after spec/status changes can lag)."""

    def _ok() -> bool:
        mdb.load()
        mig = _status_dict(mdb).get("migration")
        return isinstance(mig, dict) and mig.get("phase") == expected_phase

    KubernetesTester.wait_until(_ok, timeout=timeout)


def wait_until_phase_and_migration_phase(mdb: MongoDB, phase: str, migration_phase: str, *, timeout: int = 600) -> None:
    """Poll until both status.phase and status.migration.phase match simultaneously.

    Migration lifecycle phases (Extending, Pruning) are ephemeral — they're recomputed on every
    reconcile and can flip to empty on the very next one if counts stabilize.  Checking
    status.phase and status.migration.phase in a single poll avoids the race where a second
    reconcile runs between two separate assertions.
    """

    def _ok() -> bool:
        mdb.load()
        s = _status_dict(mdb)
        if s.get("phase") != phase:
            return False
        mig = s.get("migration")
        return isinstance(mig, dict) and mig.get("phase") == migration_phase

    KubernetesTester.wait_until(_ok, timeout=timeout)


def wait_until_running_and_migration_in_progress(mdb: MongoDB, *, timeout: int = 600) -> None:
    """Poll until status.phase is Running and status.migration.phase is InProgress.

    Used after operations where counts stabilize — externalMembers still exist but nothing
    is actively extending or pruning.
    """

    def _ok() -> bool:
        mdb.load()
        s = _status_dict(mdb)
        if s.get("phase") != "Running":
            return False
        mig = s.get("migration")
        return isinstance(mig, dict) and mig.get("phase") == MIGRATION_PHASE_IN_PROGRESS

    KubernetesTester.wait_until(_ok, timeout=timeout)


def wait_until_running_and_migration_absent(mdb: MongoDB, *, timeout: int = 600) -> None:
    """Poll until status.phase is Running and status.migration is absent (nil).

    When all externalMembers are removed the operator clears status.migration (sets it to nil).
    Checks both in a single poll to avoid races with subsequent reconciles.
    """

    def _ok() -> bool:
        mdb.load()
        s = _status_dict(mdb)
        return s.get("phase") == "Running" and s.get("migration") is None

    KubernetesTester.wait_until(_ok, timeout=timeout)


def assert_migration_absent(mdb: MongoDB, timeout: int = 120) -> None:
    """Wait for and assert that status.migration is absent (nil).

    When all externalMembers are removed the operator clears status.migration (sets it to nil).
    This helper polls until the field disappears rather than asserting immediately.
    """

    def _migration_gone() -> bool:
        mdb.load()
        return _status_dict(mdb).get("migration") is None

    KubernetesTester.wait_until(_migration_gone, timeout=timeout)
