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
from kubetester.mongodb import MongoDB

# Annotation that triggers migration dry-run (connectivity validation only, no OM/StatefulSet changes).
MIGRATION_DRY_RUN_ANNOTATION = "mongodb.com/migration-dry-run"
CONDITION_NETWORK_CONNECTIVITY_VERIFIED = "NetworkConnectivityVerification"


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


def run_migration_dry_run_connectivity_passes(mdb: MongoDB, *, timeout: int = 600) -> None:
    """Set migration-dry-run annotation, wait for NetworkConnectivityVerification, then clear the annotation.

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

    mdb.load()
    ann = mdb.backing_obj.get("metadata").get("annotations")  # ty : ignore[unresolved-attribute]
    if ann is not None and MIGRATION_DRY_RUN_ANNOTATION in ann:
        ann[MIGRATION_DRY_RUN_ANNOTATION] = None
        mdb.update()


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
