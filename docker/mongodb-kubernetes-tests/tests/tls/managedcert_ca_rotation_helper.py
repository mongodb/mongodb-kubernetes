"""Helpers for the CA rotation tests of an operator-managed certificate (security.managedCertificate) replica set.

A CA rotation is observed through the per-category trust bundle ConfigMaps, the CA file paths in the
automation config, the CertificatesReady condition, the prune annotation and the leaf certificates.
The generic rotation helpers in tests/opsmanager/managedcert_helper.py (CA reissue, leaf reissue on the
new root, pod UIDs) are reused; this module adds what a replica set with both categories needs.
"""

import os
import tempfile
import threading
import time
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
from typing import Callable, Generator, Iterable, List, Optional

import pymongo
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.x509.oid import NameOID
from kubernetes import client
from kubernetes.client.rest import ApiException
from kubetester import create_or_update_secret, delete_pod, list_matching_pods, read_configmap, read_secret
from kubetester.certs import Issuer
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import with_x509
from kubetester.operator import Operator
from pymongo.errors import PyMongoError
from tests import test_logger
from tests.conftest import wait_for_cert_manager_ready
from tests.opsmanager.managedcert_helper import CA_BUNDLE_PUBLISHED_AT_ANNOTATION
from tests.tls.managedcert_helper import (
    APP_USER,
    PRUNE_PREVIOUS_CA_ANNOTATION,
    bundle_fingerprints,
    certificates_ready_condition,
    leaf_secret_names,
    read_certificate_status,
    trigger_certificate_renewal,
)

logger = test_logger.get_test_logger(__name__)

SERVER = "server"
CLIENT = "client"

# The automation config names the trust bundle files under these mounts (TLSCaMountPath and
# TLSClusterCaMountPath in pkg/util/constants.go). The file name is the bundle's content hash.
CA_MOUNT_DIR = "/mongodb-automation/tls/ca"
CLUSTER_CA_MOUNT_DIR = "/mongodb-automation/tls/clusterca"

# Only the server bundle has a fixed key, the one MongoDBUser.status.clientCertificate points
# applications to.
SERVER_BUNDLE_ALIAS_KEY = "ca-pem"

# The reason of the Issuing condition the operator sets on a leaf it moves to a rotated self-signed CA.
MANAGED_CA_ROTATED_REASON = "ManagedCARotated"

# The x509 client certificate the operator issues for the APP_USER MongoDBUser, and its secret.
USER_CERTIFICATE = f"{APP_USER}-client-cert"
USER_CERT_SECRET = f"{APP_USER}-cert"

# An unrelated annotation the tests bump to make the operator reconcile the resource again.

# The cert-manager controller, as the conftest installs it with helm.
CERT_MANAGER_NAMESPACE = "cert-manager"
CERT_MANAGER_CONTROLLER = "cert-manager"
CERT_MANAGER_CONTROLLER_SELECTOR = "app.kubernetes.io/instance=cert-manager,app.kubernetes.io/component=controller"


def previous_ca_retained(category: str) -> str:
    """The CertificatesReady sentence while the category's bundle still trusts a previous CA."""
    return f"Previous {category} CA retained in the trust bundle"


def previous_ca_not_pruned(category: str) -> str:
    """The CertificatesReady sentence when a requested prune was refused for the category."""
    return f"The previous {category} CA was not pruned"


def self_signed_ca_name(name: str, category: str) -> str:
    """The operator's self-signed CA Certificate (and its Secret) of a category."""
    return f"{name}-{category}-ca"


def bundle_config_map_name(name: str, category: str) -> str:
    return f"{name}-managed-{category}-ca-bundle"


def _as_time(value: str) -> datetime:
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def fingerprints(pems: Iterable[str]) -> set:
    """The fingerprints of every certificate in the given PEMs."""
    result: set = set()
    for pem in pems:
        found = bundle_fingerprints(pem)
        assert found, "not a PEM certificate"
        result.update(found)
    return result


def read_bundle(namespace: str, name: str, category: str) -> dict:
    """The current state of a category's trust bundle ConfigMap.

    The bundle mongod reads is the one under the latestHash key. The returned dict holds the hashes,
    the bundle, the fingerprints of its certificates, the published-at annotation and, for the server
    bundle, the ca-pem alias.
    """
    config_map = client.CoreV1Api().read_namespaced_config_map(bundle_config_map_name(name, category), namespace)
    data = config_map.data or {}
    annotations = config_map.metadata.annotations or {}
    latest_hash = data.get("latestHash", "")
    bundle = data.get(latest_hash, "") if latest_hash else ""
    return {
        "latest_hash": latest_hash,
        "previous_hash": data.get("previousHash", ""),
        "bundle": bundle,
        "certificates": set(bundle_fingerprints(bundle)),
        "published_at": annotations.get(CA_BUNDLE_PUBLISHED_AT_ANNOTATION, ""),
        "alias": data.get(SERVER_BUNDLE_ALIAS_KEY),
    }


def bundle_contains(namespace: str, name: str, category: str, ca_pem: str) -> bool:
    return fingerprints([ca_pem]).issubset(read_bundle(namespace, name, category)["certificates"])


def wait_for_bundle(
    namespace: str,
    name: str,
    category: str,
    expected_cas: List[str],
    *,
    changed_from: Optional[str] = None,
    published_after: Optional[str] = None,
    timeout: int = 900,
) -> dict:
    """Waits until a category's bundle holds exactly the expected CAs (PEMs) and returns it.

    changed_from is a latestHash the bundle must have moved away from, and published_after a
    published-at value the annotation must be later than, so a stale bundle never matches.
    """
    expected = fingerprints(expected_cas)
    result: dict = {}

    def matches() -> tuple[bool, str]:
        snapshot = read_bundle(namespace, name, category)
        if snapshot["certificates"] != expected:
            return False, f"{len(snapshot['certificates'])} certificate(s), expected {len(expected)}"
        if changed_from is not None and snapshot["latest_hash"] == changed_from:
            return False, "latestHash did not change yet"
        if published_after:
            if not snapshot["published_at"] or _as_time(snapshot["published_at"]) <= _as_time(published_after):
                return False, "published-at was not refreshed yet"
        result.update(snapshot)
        return True, "bundle matches"

    run_periodically(matches, timeout=timeout, sleep_time=5, msg=f"{category} CA bundle of {name} to hold the CAs")
    if category == SERVER:
        assert result["alias"] == result["bundle"], "the ca-pem alias must hold the current server bundle"
    return result


def ca_file_paths(mdb: MongoDB) -> tuple[str, str]:
    """The automation config's CAFilePath and clusterCAFilePath."""
    tls = mdb.get_automation_config_tester().automation_config.get("tls") or {}
    return tls.get("CAFilePath", ""), tls.get("clusterCAFilePath", "")


def wait_for_ca_file_paths(
    mdb: MongoDB, server_hash: Optional[str] = None, client_hash: Optional[str] = None, timeout: int = 900
) -> dict:
    """Waits until the automation config names the given bundle hashes as the CA files.

    The operator names the CA file after the bundle hash, so a new bundle moves the path and the
    agents restart the members onto it, one at a time and without recreating the pods.
    """
    expected = {}
    if server_hash is not None:
        expected[("CAFilePath", "CAFile")] = f"{CA_MOUNT_DIR}/{server_hash}"
    if client_hash is not None:
        expected[("clusterCAFilePath", "clusterCAFile")] = f"{CLUSTER_CA_MOUNT_DIR}/{client_hash}"
    result: dict = {}

    def updated() -> tuple[bool, str]:
        automation_config = mdb.get_automation_config_tester().automation_config
        tls = automation_config.get("tls") or {}
        for (deployment_key, process_key), path in expected.items():
            if tls.get(deployment_key) != path:
                return False, f"{deployment_key}={tls.get(deployment_key)}"
            for process in automation_config.get("processes") or []:
                process_tls = ((process.get("args2_6") or {}).get("net") or {}).get("tls") or {}
                if process_key in process_tls and process_tls[process_key] != path:
                    return False, f"{process['name']} {process_key}={process_tls[process_key]}"
        result["automation_config"] = automation_config
        return True, "CA file paths updated"

    run_periodically(updated, timeout=timeout, sleep_time=5, msg=f"{mdb.name} automation config CA file paths")
    return result["automation_config"]


def certificates_ready_message(mdb: MongoDB) -> str:
    condition = certificates_ready_condition(mdb)
    return condition.get("message", "") if condition else ""


def wait_for_certificates_ready_message(mdb: MongoDB, text: str, *, present: bool = True, timeout: int = 900) -> str:
    """Waits until the CertificatesReady message contains (or no longer contains) text."""
    run_periodically(
        lambda: (text in certificates_ready_message(mdb)) == present,
        timeout=timeout,
        sleep_time=5,
        msg=f"CertificatesReady message {'to contain' if present else 'to drop'} {text!r}",
    )
    return certificates_ready_message(mdb)


def _patch_annotation(mdb: MongoDB, key: str, value: Optional[str]):
    # A merge patch of the annotation only: it neither overwrites the spec nor races the status
    # the operator writes. None removes the annotation.
    client.CustomObjectsApi().patch_namespaced_custom_object(
        "mongodb.com", "v1", mdb.namespace, "mongodb", mdb.name, {"metadata": {"annotations": {key: value}}}
    )


def set_prune_annotation(mdb: MongoDB, value: Optional[str]):
    """Sets (or with None removes) the mongodb.com/prune-previous-ca annotation."""
    _patch_annotation(mdb, PRUNE_PREVIOUS_CA_ANNOTATION, value)


def prune_annotation(mdb: MongoDB) -> Optional[str]:
    mdb.load()
    return (mdb["metadata"].get("annotations") or {}).get(PRUNE_PREVIOUS_CA_ANNOTATION)


def wait_for_prune_annotation_removed(mdb: MongoDB, timeout: int = 1200):
    """Waits until the operator applied the prune and removed the annotation.

    A prune refused earlier is retried on the next reconcile; the operator watches the MongoDBUser client
    certificate secrets, so a user certificate moving to the new CA triggers that reconcile.
    """
    run_periodically(
        lambda: prune_annotation(mdb) is None,
        timeout=timeout,
        sleep_time=5,
        msg=f"{PRUNE_PREVIOUS_CA_ANNOTATION} removed",
    )


def assert_during(seconds: int, check: Callable[[], None], sleep_time: int = 10):
    """Runs check repeatedly for a while; it must hold every time."""
    end = time.monotonic() + seconds
    while True:
        check()
        if time.monotonic() >= end:
            return
        time.sleep(sleep_time)


def assert_prune_annotation_kept(
    mdb: MongoDB, value: str, seconds: int, check: Optional[Callable[[], None]] = None, sleep_time: int = 10
):
    """Asserts for a while that the operator keeps the prune annotation, running check each time."""

    def kept():
        assert prune_annotation(mdb) == value, f"the operator must keep {PRUNE_PREVIOUS_CA_ANNOTATION}={value}"
        if check is not None:
            check()

    assert_during(seconds, kept, sleep_time)


def read_leaf(namespace: str, secret_name: str) -> str:
    return read_secret(namespace, secret_name).get("tls.crt", "")


def signed_by(namespace: str, secret_name: str, ca_pem: str) -> bool:
    leaf = read_leaf(namespace, secret_name)
    return bool(leaf.strip()) and cert_signed_by_bundle(leaf, ca_pem)


def client_signed_secrets(name: str) -> List[str]:
    """Every certificate the client CA signs: the clusterfile and agent certs and the x509 user cert."""
    _, clusterfile, agent = leaf_secret_names(name)
    return [clusterfile, agent, USER_CERT_SECRET]


def assert_not_pruned_ahead_of_leaves(namespace: str, name: str, category: str, old_ca: str, secrets: List[str]):
    """The operator must not drop old_ca from the bundle while any of the secrets is signed by it.

    The bundle is read before the leaves: a leaf never moves back to the old CA, so a leaf seen on the old
    CA after the bundle was seen without it means the bundle was pruned ahead of that leaf.
    """
    if bundle_contains(namespace, name, category, old_ca):
        return
    still_old = [secret for secret in secrets if signed_by(namespace, secret, old_ca)]
    assert not still_old, f"the previous {category} CA was pruned while {still_old} are still signed by it"


def wait_for_leaves_signed_by(
    namespace: str, secrets: List[str], ca_pem: str, timeout: int = 1800, invariant: Optional[Callable[[], None]] = None
):
    """Waits until every secret holds a certificate signed by ca_pem, checking invariant on the way."""

    def moved() -> tuple[bool, str]:
        if invariant is not None:
            invariant()
        pending = [secret for secret in secrets if not signed_by(namespace, secret, ca_pem)]
        return not pending, f"still on another CA: {pending}"

    run_periodically(moved, timeout=timeout, sleep_time=5, msg="certificates signed by the new CA")


def wait_for_pem_matches_leaf(namespace: str, secret_name: str, timeout: int = 900):
    """Waits until the operator wrote the certificate currently in a leaf secret into its -pem secret.

    The -pem secret holds the files the pods mount, so this is the point from which the automation
    config can name the new certificate.
    """

    def matches() -> bool:
        leaf = read_leaf(namespace, secret_name).strip()
        pem = read_secret(namespace, f"{secret_name}-pem")
        latest_hash = pem.get("latestHash", "")
        return bool(leaf) and bool(latest_hash) and leaf in pem.get(latest_hash, "")

    run_periodically(matches, timeout=timeout, sleep_time=5, msg=f"{secret_name}-pem to hold the issued certificate")


def wait_for_issuing_reason(namespace: str, certificate_name: str, reason: str, timeout: int = 1800):
    """Waits until a Certificate has Issuing=True with the given reason."""

    def issuing() -> bool:
        conditions = read_certificate_status(namespace, certificate_name).get("conditions", [])
        return any(
            c.get("type") == "Issuing" and c.get("status") == "True" and c.get("reason") == reason for c in conditions
        )

    run_periodically(issuing, timeout=timeout, sleep_time=5, msg=f"Certificate/{certificate_name} Issuing={reason}")


def renew_until_signed_by(namespace: str, certificate_name: str, secret_name: str, ca_pem: str, timeout: int = 900):
    """Renews a leaf manually until it is signed by ca_pem.

    cert-manager reads an Issuer's CA through its informer cache, so a renewal triggered right after the
    CA was replaced can still be signed by the previous one: the renewal is triggered again until the
    certificate is signed by the expected CA.
    """

    def renewed() -> bool:
        if signed_by(namespace, secret_name, ca_pem):
            return True
        conditions = read_certificate_status(namespace, certificate_name).get("conditions", [])
        if not any(c.get("type") == "Issuing" and c.get("status") == "True" for c in conditions):
            trigger_certificate_renewal(namespace, certificate_name)
        return False

    run_periodically(renewed, timeout=timeout, sleep_time=5, msg=f"Certificate/{certificate_name} on the new CA")


def member_cert_covers(namespace: str, name: str, members: int) -> bool:
    """The member certificate names every member up to the given count."""
    member_secret, _, _ = leaf_secret_names(name)
    certificate = x509.load_pem_x509_certificate(read_leaf(namespace, member_secret).encode())
    dns_names = certificate.extensions.get_extension_for_class(x509.SubjectAlternativeName).value
    names = dns_names.get_values_for_type(x509.DNSName)
    return all(any(dns.startswith(f"{name}-{index}.") for dns in names) for index in range(members))


@contextmanager
def cert_manager_controller_paused() -> Generator[None, None, None]:
    """Scales the cert-manager controller to zero, so nothing is issued until the block ends.

    The webhook keeps running, so Certificates can still be created and updated (also by the
    operator); they are only not issued. The controller is scaled back up and waited for on exit.
    """
    apps = client.AppsV1Api()
    replicas = apps.read_namespaced_deployment(CERT_MANAGER_CONTROLLER, CERT_MANAGER_NAMESPACE).spec.replicas or 1
    apps.patch_namespaced_deployment_scale(CERT_MANAGER_CONTROLLER, CERT_MANAGER_NAMESPACE, {"spec": {"replicas": 0}})
    try:
        run_periodically(
            lambda: not list_matching_pods(CERT_MANAGER_NAMESPACE, label_selector=CERT_MANAGER_CONTROLLER_SELECTOR),
            timeout=300,
            sleep_time=3,
            msg="cert-manager controller pods to be gone",
        )
        yield
    finally:
        apps.patch_namespaced_deployment_scale(
            CERT_MANAGER_CONTROLLER, CERT_MANAGER_NAMESPACE, {"spec": {"replicas": replicas}}
        )
        wait_for_cert_manager_ready()


def restart_operator_pods(operator: Operator, timeout: int = 600):
    """Deletes the operator pods, the way a crash or an eviction would, and waits for a new one."""
    deleted = {pod.metadata.name for pod in operator.list_operator_pods()}
    assert deleted, "the operator must run in a pod to be restarted"
    for pod_name in deleted:
        delete_pod(operator.namespace, pod_name)

    def replaced() -> bool:
        pods = operator.list_operator_pods()
        if len(pods) != 1 or pods[0].metadata.name in deleted:
            return False
        statuses = pods[0].status.container_statuses or []
        return pods[0].status.phase == "Running" and bool(statuses) and all(s.ready for s in statuses)

    run_periodically(replaced, timeout=timeout, sleep_time=3, msg="a new operator pod to be ready")


def generate_ca(common_name: str) -> tuple[str, str]:
    """A new self-signed root CA, as (certificate PEM, private key PEM)."""
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    subject = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, common_name)])
    now = datetime.now(timezone.utc)
    certificate = (
        x509.CertificateBuilder()
        .subject_name(subject)
        .issuer_name(subject)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(now - timedelta(minutes=5))
        .not_valid_after(now + timedelta(days=365))
        .add_extension(x509.BasicConstraints(ca=True, path_length=None), critical=True)
        .add_extension(
            x509.KeyUsage(
                digital_signature=True,
                content_commitment=False,
                key_encipherment=False,
                data_encipherment=False,
                key_agreement=False,
                key_cert_sign=True,
                crl_sign=True,
                encipher_only=False,
                decipher_only=False,
            ),
            critical=True,
        )
        .add_extension(x509.SubjectKeyIdentifier.from_public_key(key.public_key()), critical=False)
        .sign(key, hashes.SHA256())
    )
    certificate_pem = certificate.public_bytes(serialization.Encoding.PEM).decode()
    key_pem = key.private_bytes(
        serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()
    ).decode()
    return certificate_pem, key_pem


def replace_issuer_ca(namespace: str, issuer_name: str, ca_pem: str, key_pem: str, timeout: int = 300) -> str:
    """Replaces the CA key pair of a CA Issuer, keeping the Issuer, and returns the previous CA.

    This is how a customer rotates their CA: the Issuer and its Secret keep their names, the Secret gets
    a new key pair, and cert-manager signs every new issuance with it.
    """
    issuer = Issuer(name=issuer_name, namespace=namespace).load()
    secret_name = issuer["spec"]["ca"]["secretName"]
    previous_ca = read_secret(namespace, secret_name)["tls.crt"]
    create_or_update_secret(namespace, secret_name, {"tls.crt": ca_pem, "tls.key": key_pem})
    run_periodically(
        lambda: bool(Issuer(name=issuer_name, namespace=namespace).is_ready()),
        timeout=timeout,
        sleep_time=3,
        msg=f"Issuer/{issuer_name} to be ready with the new CA",
    )
    return previous_ca


class ConnectivityMonitor:
    """Connects as the x509 user in the background, the way an application keeps using the database.

    Every probe reads the user's current certificate and the published server CA bundle, connects and
    writes, so it sees what an application that reloads its files sees. A member restart can make a
    probe fail while a new primary is elected; an outage is a run of failed probes, and the tests only
    accept short ones.
    """

    def __init__(
        self,
        connection_string: str,
        namespace: str,
        user_secret: str,
        server_ca_config_map: str,
        interval: float = 5,
        server_selection_timeout_ms: int = 30000,
    ):
        self._connection_string = connection_string
        self._namespace = namespace
        self._user_secret = user_secret
        self._server_ca_config_map = server_ca_config_map
        self._interval = interval
        self._server_selection_timeout_ms = server_selection_timeout_ms
        # (probe start, success, error) for every probe that reached the database
        self._probes: list[tuple[float, bool, str]] = []
        self._lock = threading.Lock()
        self._stop = threading.Event()
        self._thread = threading.Thread(target=self._run, name="x509-connectivity-monitor", daemon=True)

    @classmethod
    def for_user(cls, mdb: MongoDB, user: MongoDBUser, namespace: str) -> "ConnectivityMonitor":
        mdb.load()
        user.load()
        cert_status = user["status"]["clientCertificate"]
        return cls(
            mdb.tester(use_ssl=True).cnx_string,
            namespace,
            cert_status["secretName"],
            cert_status["serverCAConfigMapName"],
        )

    def start(self) -> "ConnectivityMonitor":
        self._thread.start()
        return self

    def stop(self):
        self._stop.set()
        self._thread.join(timeout=120)

    def _run(self):
        while not self._stop.is_set():
            started = time.time()
            try:
                self._probe()
                self._record(started, True, "")
            except PyMongoError as e:
                self._record(started, False, str(e))
            except ApiException as e:
                # Not reaching the Kubernetes API says nothing about the database.
                logger.warning(f"connectivity probe could not read the user certificate: {e}")
            except Exception as e:
                logger.warning(f"connectivity probe could not run: {e}")
            self._stop.wait(self._interval)

    def _probe(self):
        cert = read_secret(self._namespace, self._user_secret)
        server_ca = read_configmap(self._namespace, self._server_ca_config_map)[SERVER_BUNDLE_ALIAS_KEY]
        with (
            tempfile.NamedTemporaryFile(mode="w", suffix=".pem", delete=False) as cert_file,
            tempfile.NamedTemporaryFile(mode="w", suffix=".pem", delete=False) as ca_file,
        ):
            cert_file.write(cert["tls.crt"] + cert["tls.key"])
            ca_file.write(server_ca)
        try:
            options = with_x509(cert_file.name, ca_file.name)
            options["serverSelectionTimeoutMS"] = self._server_selection_timeout_ms
            mongo_client: pymongo.MongoClient = pymongo.MongoClient(self._connection_string, **options)
            try:
                mongo_client["admin"]["caRotationProbe"].insert_one({"at": datetime.now(timezone.utc)})
            finally:
                mongo_client.close()
        finally:
            os.unlink(cert_file.name)
            os.unlink(ca_file.name)

    def _record(self, started: float, ok: bool, error: str):
        with self._lock:
            self._probes.append((started, ok, error))
        if not ok:
            logger.warning(f"x509 connectivity probe failed: {error}")

    def longest_outage(self) -> tuple[float, str]:
        """The longest run of failed probes in seconds, and the last error of that run."""
        with self._lock:
            probes = list(self._probes)
        longest, longest_error = 0.0, ""
        outage_start: Optional[float] = None
        last_error = ""
        for started, ok, error in probes:
            if ok:
                if outage_start is not None and started - outage_start > longest:
                    longest, longest_error = started - outage_start, last_error
                outage_start = None
            else:
                outage_start = started if outage_start is None else outage_start
                last_error = error
        if outage_start is not None and time.time() - outage_start > longest:
            longest, longest_error = time.time() - outage_start, last_error
        return longest, longest_error

    def assert_no_outage(self, max_outage_seconds: int):
        """The x509 user must not have been locked out for longer than max_outage_seconds so far."""
        assert self._thread.is_alive(), "the connectivity monitor stopped"
        with self._lock:
            succeeded = sum(1 for _, ok, _ in self._probes if ok)
            total = len(self._probes)
        assert succeeded > 0, "the x509 user never connected"
        longest, error = self.longest_outage()
        logger.info(f"x509 connectivity: {succeeded}/{total} probes succeeded, longest outage {longest:.0f}s")
        assert (
            longest <= max_outage_seconds
        ), f"the x509 user could not connect for {longest:.0f}s (allowed {max_outage_seconds}s): {error}"
