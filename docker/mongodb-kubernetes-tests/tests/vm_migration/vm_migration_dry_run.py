"""Shared VM migration dry-run (connectivity-only) flow for plain-TLS and X509 E2E tests."""

import datetime
import time

from cryptography import x509 as crypto_x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.x509.oid import NameOID
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import create_or_update_configmap
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB

# Annotation that triggers migration dry-run (connectivity validation only, no OM/StatefulSet changes).
MIGRATION_DRY_RUN_ANNOTATION = "mongodb.com/migration-dry-run"
# Condition type the operator writes for the connectivity dry-run (api/v1/status ConditionNetworkConnectivityVerified).
CONDITION_NETWORK_CONNECTIVITY_VERIFIED = "NetworkConnectivityVerified"
# Top-level condition indicating whether VM-to-K8s migration is active.
CONDITION_MIGRATING = "Migrating"
# Literal status.conditions[Migrating].reason values while Migrating=True (api/v1/status MigratingReason*).
MIGRATING_CONDITION_REASON_VALIDATING = "Validating"
MIGRATING_CONDITION_REASON_EXTENDING = "Extending"
MIGRATING_CONDITION_REASON_PRUNING = "Pruning"
MIGRATING_CONDITION_REASON_IN_PROGRESS = "InProgress"
MIGRATING_CONDITION_REASON_COMPLETE = "MigrationComplete"


def _status_dict(mdb: MongoDB) -> dict:
    try:
        s = mdb["status"]
    except (KeyError, AttributeError, TypeError):
        return {}
    return s if isinstance(s, dict) else {}


def _get_condition(conditions, condition_type: str) -> dict | None:
    """Find a condition by type in a list of conditions."""
    if not conditions:
        return None
    for c in conditions:
        if c.get("type") == condition_type:
            return c
    return None


def _migration_observed_external_count(s: dict) -> int:
    """Last reconciled externalMembers count from status (0 if unset / null / omitempty)."""
    v = s.get("migrationObservedExternalMembersCount")
    if isinstance(v, int):
        return v
    return 0


def _network_connectivity_true_in_conditions(conditions) -> bool:
    return _get_condition(conditions, CONDITION_NETWORK_CONNECTIVITY_VERIFIED) is not None and any(
        c.get("type") == CONDITION_NETWORK_CONNECTIVITY_VERIFIED and c.get("status") == "True" for c in conditions
    )


def generate_wrong_ca_pem() -> str:
    """Return a self-signed CA certificate PEM that did not sign any of the VM server certs."""
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    name = crypto_x509.Name([crypto_x509.NameAttribute(NameOID.COMMON_NAME, "wrong-test-ca")])
    now = datetime.datetime.utcnow()
    cert = (
        crypto_x509.CertificateBuilder()
        .subject_name(name)
        .issuer_name(name)
        .public_key(key.public_key())
        .serial_number(crypto_x509.random_serial_number())
        .not_valid_before(now)
        .not_valid_after(now + datetime.timedelta(days=365))
        .add_extension(crypto_x509.BasicConstraints(ca=True, path_length=None), critical=True)
        .sign(key, hashes.SHA256())
    )
    return cert.public_bytes(serialization.Encoding.PEM).decode("utf-8")


def create_wrong_ca_configmap(namespace: str, wrong_ca_name: str) -> None:
    wrong_ca_pem = generate_wrong_ca_pem()
    create_or_update_configmap(namespace, wrong_ca_name, {"ca-pem": wrong_ca_pem, "mms-ca.crt": wrong_ca_pem})


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
    ), "expected NetworkConnectivityVerified status True on status.conditions"
    assert "migration" not in status or status.get("migration") in (
        None,
        {},
    ), "status.migration must not be set; migration state lives under status.conditions"


def run_migration_dry_run_connectivity_passes(mdb: MongoDB, *, timeout: int = 600) -> None:
    """Dry-run annotation → wait for connectivity → assert ``Validating`` → clear annotation.

    While the annotation is present, ``Migrating`` is True with ``reason: Validating``; connectivity
    progress is in ``NetworkConnectivityVerified`` on ``status.conditions``. After the annotation is
    removed, the operator transitions to ``InProgress`` on the next reconcile.

    Removes the dry-run annotation so later tests reconcile normally. Uses backing_obj and JSON merge
    patch (null) so the key is actually removed. Merge patch only drops keys when set to null.
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
    ann = mdb.backing_obj.get("metadata").get("annotations")  # ty : ignore[unresolved-attribute]
    if ann is not None and MIGRATION_DRY_RUN_ANNOTATION in ann:
        ann[MIGRATION_DRY_RUN_ANNOTATION] = None
        mdb.update()
    wait_until_migrating_condition_reason(mdb, MIGRATING_CONDITION_REASON_IN_PROGRESS)


def wait_until_migrating_condition_reason(mdb: MongoDB, expected_reason: str, *, timeout: int = 120) -> None:
    """Poll until Migrating=True and Migrating.reason matches expected_reason."""

    def _ok() -> bool:
        mdb.load()
        mig = _get_condition(_status_dict(mdb).get("conditions", []), CONDITION_MIGRATING)
        return mig is not None and mig.get("status") == "True" and mig.get("reason") == expected_reason

    KubernetesTester.wait_until(_ok, timeout=timeout)


def wait_until_phase_and_migrating_condition_reason(
    mdb: MongoDB, phase: str, migrating_reason: str, *, timeout: int = 600
) -> None:
    """Poll until status.phase, Migrating=True, and Migrating.reason all match.

    Migrating reasons (Extending, Pruning, …) are ephemeral — they're recomputed on every
    reconcile and can flip on the very next one if counts stabilize. Checking status.phase and
    Migrating.reason in a single poll avoids the race where a second reconcile runs between two
    separate assertions.
    """

    def _ok() -> bool:
        mdb.load()
        s = _status_dict(mdb)
        if s.get("phase") != phase:
            return False
        mig = _get_condition(s.get("conditions", []), CONDITION_MIGRATING)
        return mig is not None and mig.get("status") == "True" and mig.get("reason") == migrating_reason

    KubernetesTester.wait_until(_ok, timeout=timeout)


def wait_until_running_and_migration_in_progress(mdb: MongoDB, *, timeout: int = 600) -> None:
    """Poll until status.phase is Running and Migrating.reason is InProgress.

    Used after operations where counts stabilize — externalMembers still exist but nothing is
    actively extending or pruning.
    """
    wait_until_phase_and_migrating_condition_reason(
        mdb, "Running", MIGRATING_CONDITION_REASON_IN_PROGRESS, timeout=timeout
    )


def wait_until_running_and_migration_complete(mdb: MongoDB, *, timeout: int = 600) -> None:
    """Poll until status.phase is Running and migration is complete (Migrating=False).

    When all externalMembers are removed the operator unsets ``migrationObservedExternalMembersCount``,
    removes ``NetworkConnectivityVerified``, and sets ``Migrating=False, reason=MigrationComplete``.
    """

    def _ok() -> bool:
        mdb.load()
        s = _status_dict(mdb)
        if s.get("phase") != "Running":
            return False
        if _migration_observed_external_count(s) != 0:
            return False
        cond = _get_condition(s.get("conditions", []), CONDITION_MIGRATING)
        return (
            cond is not None
            and cond.get("status") == "False"
            and cond.get("reason") == MIGRATING_CONDITION_REASON_COMPLETE
        )

    KubernetesTester.wait_until(_ok, timeout=timeout)


def _migration_connectivity_failed(mdb: MongoDB) -> bool:
    try:
        status = mdb["status"]
    except (KeyError, AttributeError, TypeError):
        status = {}
    conditions = status.get("conditions", []) if isinstance(status, dict) else []
    for c in conditions:
        if c.get("type") == CONDITION_NETWORK_CONNECTIVITY_VERIFIED and c.get("status") == "False":
            return True
    return False


def run_migration_dry_run_connectivity_fails(mdb: MongoDB, *, timeout: int = 300) -> None:
    """Set migration-dry-run annotation and wait for NetworkConnectivityVerification to be False.

    Does not remove the annotation so the caller can fix the root cause and re-trigger validation
    by deleting the failed Job and updating the MDB resource.
    """
    mdb.load()
    if "metadata" not in mdb:
        mdb["metadata"] = {}
    if "annotations" not in mdb["metadata"]:
        mdb["metadata"]["annotations"] = {}
    mdb["metadata"]["annotations"][MIGRATION_DRY_RUN_ANNOTATION] = "true"
    mdb.update()

    mdb.wait_for(_migration_connectivity_failed, timeout=timeout)


def _delete_connectivity_job_if_exists(namespace: str, job_name: str, timeout: int = 120) -> None:
    batch_api = client.BatchV1Api()
    try:
        batch_api.delete_namespaced_job(
            job_name,
            namespace,
            body=client.V1DeleteOptions(propagation_policy="Background"),
        )
    except ApiException as e:
        if e.status != 404:
            raise

    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            batch_api.read_namespaced_job(job_name, namespace)
        except ApiException as e:
            if e.status == 404:
                return
            raise
        time.sleep(3)
    raise AssertionError(f"Timed out waiting for connectivity Job {namespace}/{job_name} to be deleted")


def run_wrong_ca_dry_run_fails_then_passes(
    namespace: str,
    mdb: MongoDB,
    connectivity_job_name: str,
    wrong_ca_name: str,
    correct_ca_name: str | None = None,
) -> None:
    """Verify migration dry-run fails with an existing invalid CA, then passes after restoring the valid CA."""
    mdb.load()
    original_ca_name = mdb["spec"]["security"]["tls"]["ca"]
    mdb["spec"]["security"]["tls"]["ca"] = wrong_ca_name
    mdb.update()
    run_migration_dry_run_connectivity_fails(mdb)

    _delete_connectivity_job_if_exists(namespace, connectivity_job_name)
    mdb.load()
    mdb["spec"]["security"]["tls"]["ca"] = correct_ca_name or original_ca_name
    mdb.update()
    run_migration_dry_run_connectivity_passes(mdb)
