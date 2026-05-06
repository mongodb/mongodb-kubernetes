"""Shared VM migration dry-run (connectivity-only) flow for plain-TLS and X509 E2E tests."""

from kubetester.mongodb import MongoDB

# Annotation that triggers migration dry-run (connectivity validation only, no OM/StatefulSet changes).
MIGRATION_DRY_RUN_ANNOTATION = "mongodb.com/migration-dry-run"
CONDITION_NETWORK_CONNECTIVITY_VERIFIED = "NetworkConnectivityVerification"


def _migration_connectivity_passed(mdb: MongoDB) -> bool:
    # MongoDB (CustomObject) supports [] but not .get(); status/conditions come from the API.
    try:
        status = mdb["status"]
    except (KeyError, AttributeError, TypeError):
        status = {}
    conditions = status.get("conditions", []) if isinstance(status, dict) else []
    for c in conditions:
        if c.get("type") == CONDITION_NETWORK_CONNECTIVITY_VERIFIED and c.get("status") == "True":
            return True
    return False


def run_migration_dry_run_connectivity_passes(mdb: MongoDB, *, timeout: int = 600) -> None:
    """Set migration-dry-run annotation, wait for NetworkConnectivityVerification, then clear the annotation.

    Removes the dry-run annotation so later tests reconcile normally. Uses backing_obj and JSON merge
    patch (null) so the key is actually removed—merge patch only drops keys when set to null.
    """
    mdb.load()
    if "metadata" not in mdb:
        mdb["metadata"] = {}
    if "annotations" not in mdb["metadata"]:
        mdb["metadata"]["annotations"] = {}
    mdb["metadata"]["annotations"][MIGRATION_DRY_RUN_ANNOTATION] = "true"
    mdb.update()

    mdb.wait_for(_migration_connectivity_passed, timeout=timeout)

    mdb.load()
    ann = mdb.backing_obj.get("metadata").get("annotations")
    if ann is not None and MIGRATION_DRY_RUN_ANNOTATION in ann:
        ann[MIGRATION_DRY_RUN_ANNOTATION] = None
        mdb.update()
