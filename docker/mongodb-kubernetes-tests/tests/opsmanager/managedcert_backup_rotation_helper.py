"""Helpers for the managed-certificate renewal and queryable backup CA rotation suites.

The generic rotation helpers (bundle snapshots, CA rotation, prune, CertificatesReady messages)
live in tests.opsmanager.managedcert_helper. This module adds what those helpers do not cover:

- the queryable backup certificate, whose derived Secret holds a single queryable.pem instead
  of a hashed -pem Secret;
- snapshot queries that retry while the backup daemon picks up a new queryable.pem;
- the certificate a TLS endpoint actually serves, to tell a renewed leaf from a cached one;
- renewal status checks against cert-manager's own Certificate status.
"""

import ssl
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from urllib.parse import urlparse

import kubernetes
import pytest
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import NameOID
from kubernetes.client.rest import ApiException
from kubetester import get_default_storage_class, read_secret
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.opsmanager import MongoDBOpsManager
from pymongo.errors import ServerSelectionTimeoutError
from tests.opsmanager.managedcert_helper import (
    APP_DB_CA_BUNDLE_KEY,
    CA_BUNDLE_KEY,
    ManagedCertificateNames,
    ca_bundle_snapshot,
    read_component_certificate_renewal,
)
from tests.tls.managedcert_helper import bundle_fingerprints, read_certificate_status

QUERYABLE_PEM_KEY = "queryable.pem"
OM_PEM_KEY_FILE_ENV_VAR = "OM_PROP_mms_https_PEMKeyFile"
OM_PEM_KEY_FILE_DIR = "/opt/mongodb/mms/secrets"
APP_DB_PORT = 27017

# The operator re-issues a leaf on a rotated self-signed CA only once the widened bundle has been
# published for this long, so the mounted ConfigMaps had time to propagate.
CA_BUNDLE_PROPAGATION_DELAY = timedelta(minutes=2)

# The query failure an untrusted queryable backup certificate produces.
CERTIFICATE_VERIFY_FAILED = "CERTIFICATE_VERIFY_FAILED|certificate verify failed"


@dataclass(frozen=True)
class BackupCertificateNames:
    """The objects the operator derives for the queryable backup managed certificate."""

    om_name: str

    @property
    def ca_certificate(self) -> str:
        return f"{self.om_name}-managed-backup-server-ca"

    @property
    def ca_issuer(self) -> str:
        return f"{self.om_name}-managed-backup-server-ca-issuer"

    @property
    def leaf_certificate(self) -> str:
        return f"{self.om_name}-managed-backup-cert"

    @property
    def ca_bundle_config_map(self) -> str:
        return f"{self.om_name}-managed-backup-ca-bundle"

    @property
    def pem_secret(self) -> str:
        return f"{self.om_name}-managed-backup-pem"


def parse_time(value: str) -> datetime:
    """Parses an RFC3339 timestamp as Kubernetes writes it."""
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def queryable_backup_ops_manager(
    name: str,
    namespace: str,
    custom_version: str,
    custom_appdb_version: str,
    oplog_store: str,
    block_store: str,
    managed_certificate: dict,
) -> MongoDBOpsManager:
    """The Ops Manager the queryable backup managed-certificate suites deploy.

    It mirrors the fixture of om_ops_manager_queryable_backup_managedcert: plain HTTP Ops Manager,
    one backup daemon, MongoDB-backed oplog and block stores, and spec.backup.managedCertificate.
    """
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_backup.yaml"), namespace=namespace, name=name)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()
    backup = resource["spec"]["backup"]
    backup.pop("s3Stores", None)
    backup["members"] = 1
    backup["headDB"]["storageClass"] = get_default_storage_class()
    backup["opLogStores"][0]["mongodbResourceRef"]["name"] = oplog_store
    backup["blockStores"][0]["mongodbResourceRef"]["name"] = block_store
    backup["managedCertificate"] = managed_certificate
    resource["spec"]["configuration"]["mongodb.release.autoDownload.enterprise"] = "true"
    resource.update()
    return resource


# --- Certificates ----------------------------------------------------------------


def leaf_issuance(namespace: str, certificate_name: str) -> tuple[int, str]:
    """The cert-manager revision of a leaf Certificate and its current tls.crt."""
    revision = read_certificate_status(namespace, certificate_name).get("revision", 0)
    return revision, read_secret(namespace, certificate_name).get("tls.crt", "")


def wait_for_leaf_reissued(
    namespace: str,
    certificate_name: str,
    previous_revision: int,
    previous_leaf: str,
    signed_by: str | None = None,
    timeout: int = 900,
) -> tuple[int, str]:
    """Waits until cert-manager issued a new leaf, optionally signed by the given CA bundle.

    Returns the new revision and leaf PEM.
    """
    result: dict = {}

    def reissued() -> tuple[bool, str]:
        revision, leaf = leaf_issuance(namespace, certificate_name)
        if revision <= previous_revision or not leaf.strip() or leaf == previous_leaf:
            return False, f"revision {revision}"
        if signed_by is not None and not cert_signed_by_bundle(leaf, signed_by):
            return False, f"revision {revision} is not signed by the expected CA yet"
        result["revision"] = revision
        result["leaf"] = leaf
        return True, f"revision {revision}"

    run_periodically(reissued, timeout=timeout, sleep_time=5, msg=f"Certificate/{certificate_name} to be reissued")
    return result["revision"], result["leaf"]


def certificate_fingerprint(cert_pem: str) -> str:
    """The SHA-256 fingerprint of the first certificate of a PEM."""
    fingerprints = bundle_fingerprints(cert_pem)
    assert fingerprints, "not a PEM certificate"
    return fingerprints[0]


def assert_issued_after_bundle_propagation(namespace: str, certificate_name: str, published_at: str):
    """The leaf must not be issued before the widened bundle had time to propagate.

    cert-manager sets the leaf notBefore to the issuance time, so it is the time the operator
    triggered the re-issue (plus the issuance latency).
    """
    not_before = read_certificate_status(namespace, certificate_name).get("notBefore", "")
    assert not_before, f"Certificate/{certificate_name} reports no notBefore"
    earliest = parse_time(published_at) + CA_BUNDLE_PROPAGATION_DELAY
    # cert-manager truncates notBefore to seconds; allow for that and minor clock differences.
    assert parse_time(not_before) >= earliest - timedelta(seconds=10), (
        f"Certificate/{certificate_name} was issued at {not_before}, before the widened bundle "
        f"published at {published_at} could propagate"
    )


# --- Queryable backup PEM --------------------------------------------------------


def queryable_pem(namespace: str, names: BackupCertificateNames) -> str:
    return read_secret(namespace, names.pem_secret).get(QUERYABLE_PEM_KEY, "")


def backup_ca_bundle(namespace: str, names: BackupCertificateNames) -> str:
    data = kubernetes.client.CoreV1Api().read_namespaced_config_map(names.ca_bundle_config_map, namespace).data or {}
    return data.get(CA_BUNDLE_KEY, "")


def queryable_pem_is_current(namespace: str, names: BackupCertificateNames) -> tuple[bool, str]:
    """queryable.pem holds the issued leaf, its key and the full published backup CA bundle."""
    issued = read_secret(namespace, names.leaf_certificate)
    pem = queryable_pem(namespace, names)
    bundle = backup_ca_bundle(namespace, names)
    if not pem.strip() or not bundle.strip():
        return False, "queryable.pem or the backup CA bundle is empty"
    if issued.get("tls.crt", "") not in pem or issued.get("tls.key", "") not in pem:
        return False, "queryable.pem does not hold the issued leaf and key"
    if not set(bundle_fingerprints(bundle)).issubset(set(bundle_fingerprints(pem))):
        return False, "queryable.pem does not hold every CA of the backup bundle"
    return True, "queryable.pem is current"


def wait_for_queryable_pem(
    namespace: str, names: BackupCertificateNames, previous_pem: str = "", timeout: int = 600
) -> str:
    """Waits until queryable.pem matches the issued leaf and the bundle, and differs from previous_pem."""

    def current() -> tuple[bool, str]:
        ok, message = queryable_pem_is_current(namespace, names)
        if not ok:
            return False, message
        if previous_pem and queryable_pem(namespace, names) == previous_pem:
            return False, "queryable.pem has not changed yet"
        return True, message

    run_periodically(current, timeout=timeout, sleep_time=5, msg=f"{names.pem_secret} to be refreshed")
    return queryable_pem(namespace, names)


# --- Snapshot queries ------------------------------------------------------------


def unrelated_ca_pem() -> str:
    """A self-signed CA that never signed anything the operator issued."""
    key = ec.generate_private_key(ec.SECP256R1())
    subject = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "unrelated-queryable-ca")])
    now = datetime.now(timezone.utc)
    return (
        x509.CertificateBuilder()
        .subject_name(subject)
        .issuer_name(subject)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(now - timedelta(minutes=1))
        .not_valid_after(now + timedelta(days=1))
        .add_extension(x509.BasicConstraints(ca=True, path_length=None), critical=True)
        .sign(key, hashes.SHA256())
        .public_bytes(serialization.Encoding.PEM)
        .decode()
    )


def wait_for_snapshot_query(
    ops_manager: MongoDBOpsManager,
    project_name: str,
    replica_set_name: str,
    database: str,
    collection: str,
    document: dict,
    ca_pem: str | None,
    timeout: int = 1200,
):
    """Queries the snapshot, trusting only ca_pem, until the expected document is returned.

    The backup daemon and Ops Manager read the mounted queryable.pem, and the kubelet syncs a
    changed Secret into the pods only after its sync period, so a query right after a certificate
    change may still meet the previous certificate.
    """

    def queried() -> tuple[bool, str]:
        tester = ops_manager.get_om_tester(project_name=project_name)
        try:
            records = tester.query_backup(
                database,
                collection,
                600,
                ca_pem=ca_pem,
                replica_set_name=replica_set_name,
                server_selection_timeout_ms=30000,
            )
        except Exception as exception:
            return False, f"{type(exception).__name__}: {str(exception)[:300]}"
        return document in records, f"records {records}"

    run_periodically(queried, timeout=timeout, sleep_time=10, msg="the snapshot query to return the document")


def assert_snapshot_query_rejected(
    ops_manager: MongoDBOpsManager,
    project_name: str,
    replica_set_name: str,
    database: str,
    collection: str,
    ca_pem: str,
):
    """A query that trusts only ca_pem fails the TLS handshake with the queryable endpoint."""
    tester = ops_manager.get_om_tester(project_name=project_name)
    with pytest.raises(ServerSelectionTimeoutError, match=CERTIFICATE_VERIFY_FAILED):
        tester.query_backup(
            database,
            collection,
            600,
            ca_pem=ca_pem,
            replica_set_name=replica_set_name,
            server_selection_timeout_ms=5000,
        )


# --- Bundles of the other components ---------------------------------------------


def other_component_bundles(namespace: str, ops_manager: MongoDBOpsManager) -> dict:
    """Snapshots of the Ops Manager HTTPS and AppDB bundles, None for a bundle that does not exist."""
    names = ManagedCertificateNames.for_ops_manager(ops_manager)
    snapshots: dict = {}
    for config_map, key in (
        (names.om_ca_bundle_config_map, CA_BUNDLE_KEY),
        (names.app_db_ca_bundle_config_map, APP_DB_CA_BUNDLE_KEY),
    ):
        try:
            snapshots[config_map] = ca_bundle_snapshot(namespace, config_map, key)
        except ApiException as exception:
            if exception.status != 404:
                raise
            snapshots[config_map] = None
    return snapshots


# --- Renewal status --------------------------------------------------------------


def wait_for_renewal_status_matching_certificate(
    ops_manager: MongoDBOpsManager,
    status_field: str,
    certificate_name: str,
    newer_than: str = "",
    timeout: int = 900,
) -> dict:
    """Waits until status.<field>.certificateRenewals.server reports cert-manager's own schedule.

    notAfter and renewalTime must equal the Certificate status. With newer_than the reported
    notAfter must also be later, which proves the status followed a renewal.
    """
    namespace = ops_manager.namespace
    result: dict = {}

    def matches() -> tuple[bool, str]:
        renewal = read_component_certificate_renewal(ops_manager, status_field) or {}
        certificate_status = read_certificate_status(namespace, certificate_name)
        if renewal.get("name") != certificate_name:
            return False, f"reported {renewal}"
        for field in ("notAfter", "renewalTime"):
            if not renewal.get(field) or not certificate_status.get(field):
                return False, f"{field} missing: reported {renewal}, cert-manager {certificate_status.get(field)}"
            if parse_time(renewal[field]) != parse_time(certificate_status[field]):
                return False, f"{field} {renewal[field]} differs from cert-manager {certificate_status[field]}"
        if newer_than and parse_time(renewal["notAfter"]) <= parse_time(newer_than):
            return False, f"notAfter {renewal['notAfter']} is not past {newer_than}"
        result["renewal"] = renewal
        return True, f"reported {renewal}"

    run_periodically(
        matches,
        timeout=timeout,
        sleep_time=5,
        msg=f"{status_field}.certificateRenewals.server to match Certificate/{certificate_name}",
    )
    return result["renewal"]


# --- Served certificates and pods ------------------------------------------------


def served_certificate(host: str, port: int, timeout: int = 10) -> str:
    """The PEM of the leaf a TLS endpoint serves, without verifying it."""
    return ssl.get_server_certificate((host, port), timeout=timeout)


def wait_for_served_leaf(host: str, port: int, leaf_pem: str, timeout: int = 900):
    """Waits until the endpoint serves the given leaf certificate."""
    expected = certificate_fingerprint(leaf_pem)

    def serving() -> tuple[bool, str]:
        try:
            served = certificate_fingerprint(served_certificate(host, port))
        except OSError as exception:
            return False, f"{type(exception).__name__}: {exception}"
        return served == expected, f"serving {served}"

    run_periodically(serving, timeout=timeout, sleep_time=5, msg=f"{host}:{port} to serve the renewed certificate")


def ops_manager_endpoint(ops_manager: MongoDBOpsManager) -> tuple[str, int]:
    url = ops_manager.om_status().get_url()
    assert url, "Ops Manager URL is not reported"
    parsed = urlparse(url)
    assert parsed.scheme == "https", f"Ops Manager is not served over HTTPS: {url}"
    assert parsed.hostname, f"Ops Manager URL has no host: {url}"
    return parsed.hostname, parsed.port or 443


def appdb_member_hosts(ops_manager: MongoDBOpsManager) -> list[str]:
    app_db_name = ops_manager.app_db_name()
    spec = ops_manager["spec"]
    cluster_domain = (spec.get("applicationDatabase") or {}).get("clusterDomain") or spec.get(
        "clusterDomain", "cluster.local"
    )
    return [
        f"{app_db_name}-{member}.{app_db_name}-svc.{ops_manager.namespace}.svc.{cluster_domain}"
        for member in range(ops_manager.get_appdb_members_count())
    ]


def om_pod_uids(ops_manager: MongoDBOpsManager) -> dict[str, str]:
    return {pod.metadata.name: pod.metadata.uid for _, pod in ops_manager.read_om_pods()}


def wait_for_om_pods_replaced(ops_manager: MongoDBOpsManager, previous_uids: dict[str, str], timeout: int = 1800):
    """Waits until every Ops Manager pod was recreated and is ready."""

    def replaced() -> tuple[bool, str]:
        try:
            pods = [pod for _, pod in ops_manager.read_om_pods()]
        except ApiException as exception:
            if exception.status == 404:
                return False, "an Ops Manager pod is being recreated"
            raise
        for pod in pods:
            if pod.metadata.uid == previous_uids.get(pod.metadata.name):
                return False, f"{pod.metadata.name} was not recreated yet"
            statuses = pod.status.container_statuses or []
            if not statuses or not all(status.ready for status in statuses):
                return False, f"{pod.metadata.name} is not ready"
        return True, "every Ops Manager pod was recreated"

    run_periodically(replaced, timeout=timeout, sleep_time=10, msg="Ops Manager pods to roll")


def wait_for_om_pem_key_file(ops_manager: MongoDBOpsManager, pem_hash: str, timeout: int = 900):
    """Waits until the Ops Manager StatefulSet points mms.https.PEMKeyFile at the given -pem hash."""
    expected = f"{OM_PEM_KEY_FILE_DIR}/{pem_hash}"

    def configured() -> tuple[bool, str]:
        statefulset = ops_manager.read_statefulset()
        for container in statefulset.spec.template.spec.containers or []:
            for env in container.env or []:
                if env.name == OM_PEM_KEY_FILE_ENV_VAR:
                    return env.value == expected, f"{OM_PEM_KEY_FILE_ENV_VAR}={env.value}"
        return False, f"{OM_PEM_KEY_FILE_ENV_VAR} is not set"

    run_periodically(configured, timeout=timeout, sleep_time=5, msg=f"{OM_PEM_KEY_FILE_ENV_VAR} to name {pem_hash}")
