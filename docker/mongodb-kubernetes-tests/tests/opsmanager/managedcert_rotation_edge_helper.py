"""Helpers for the edge cases of the Ops Manager managed-certificate CA rotation.

The rotation building blocks (bundle snapshots, CA rotation triggers, leaf re-issue waits, the
prune annotation) live in tests.opsmanager.managedcert_helper. This module adds what the edge
case scenarios need on top: pod identity and restart tracking, the CA files the pods read from
their mounted ConfigMaps, agent check-ins with Ops Manager, a downstream replica set that trusts
the managed Ops Manager CA bundle, and an operator pod restart.
"""

import re
import socket
import ssl
from datetime import datetime, timedelta, timezone
from urllib.parse import urlparse

import kubernetes
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import NameOID
from kubernetes.client.rest import ApiException
from kubetester import list_matching_pods, read_secret, try_load
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMTester
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from tests.opsmanager.managedcert_helper import (
    CA_BUNDLE_KEY,
    OM_APP_DB_CA_FILE_PATH,
    OM_CONTAINER,
    PRUNE_PREVIOUS_CA_ANNOTATION,
    ManagedCertificateNames,
    ca_bundle_snapshot,
)
from tests.tls.managedcert_helper import bundle_fingerprints, read_certificate_status

# How long the operator waits after publishing a widened bundle before re-issuing a leaf.
MANAGED_CA_PROPAGATION_DELAY = timedelta(minutes=2)
# Tolerated clock difference between the operator, cert-manager and the test runner.
CLOCK_SKEW = timedelta(seconds=30)
# The Issuing condition reason the operator sets when it re-issues a leaf onto a rotated CA.
MANAGED_CA_ROTATED_REASON = "ManagedCARotated"

# The agent container of the AppDB pods and of static-architecture database pods.
AGENT_CONTAINER = "mongodb-agent"
# Where the AppDB and database agents read the Ops Manager CA from (-httpsCAFile and
# SSL_TRUSTED_MMS_SERVER_CERTIFICATE): the sslMMSCAConfigMap mounted at /mongodb-automation/certs.
AGENT_OM_CA_FILE_PATH = f"/mongodb-automation/certs/{CA_BUNDLE_KEY}"

STATIC_ARCHITECTURE_ANNOTATION = "mongodb.com/v1.architecture"


def parse_time(value: str) -> datetime:
    """Parses an RFC3339/ISO-8601 timestamp, as Kubernetes and Ops Manager write them, in UTC."""
    parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed


def now_utc() -> datetime:
    return datetime.now(timezone.utc)


# --- Pods -------------------------------------------------------------------------


def _pod_restart_count(pod: kubernetes.client.V1Pod) -> int:
    statuses = (pod.status.container_statuses or []) if pod.status else []
    return sum(status.restart_count or 0 for status in statuses)


def pod_states(namespace: str, name_pattern: str) -> dict[str, tuple[str, int]]:
    """UID and summed container restart count of every pod whose name fully matches the pattern."""
    regex = re.compile(name_pattern)
    return {
        pod.metadata.name: (pod.metadata.uid, _pod_restart_count(pod))
        for pod in list_matching_pods(namespace)
        if regex.fullmatch(pod.metadata.name)
    }


def statefulset_pod_pattern(statefulset_name: str) -> str:
    """Matches the pods of one StatefulSet only, so <om>-0 does not also match <om>-db-0."""
    return rf"{re.escape(statefulset_name)}-\d+"


def om_pod_states(ops_manager: MongoDBOpsManager) -> dict[str, tuple[str, int]]:
    return pod_states(ops_manager.namespace, statefulset_pod_pattern(ops_manager.name))


def appdb_pod_states(ops_manager: MongoDBOpsManager) -> dict[str, tuple[str, int]]:
    names = ManagedCertificateNames.for_ops_manager(ops_manager)
    return pod_states(ops_manager.namespace, statefulset_pod_pattern(names.app_db_name))


def assert_pods_not_recreated(
    before: dict[str, tuple[str, int]],
    after: dict[str, tuple[str, int]],
    description: str,
    *,
    check_restarts: bool = False,
):
    """The same pods (by UID) still run; with check_restarts no container restarted either."""
    assert before, f"no {description} pods were recorded"
    assert {name: uid for name, (uid, _) in after.items()} == {
        name: uid for name, (uid, _) in before.items()
    }, f"the {description} pods must not be recreated: before={before}, after={after}"
    if check_restarts:
        assert {name: restarts for name, (_, restarts) in after.items()} == {
            name: restarts for name, (_, restarts) in before.items()
        }, f"no {description} container may restart: before={before}, after={after}"


# --- Mounted CA files -------------------------------------------------------------


def read_pod_file(namespace: str, pod_name: str, container: str, path: str) -> str:
    """Reads a file inside a running container, or returns an empty string when it can't."""
    try:
        return KubernetesTester.run_command_in_pod_container(pod_name, namespace, ["cat", path], container=container)
    except ApiException:
        return ""


def wait_for_mounted_bundle(
    namespace: str,
    pod_names: list[str],
    container: str,
    path: str,
    expected_bundle_pem: str,
    timeout: int = 600,
):
    """Waits until the CA file every pod reads from its mounted ConfigMap holds the expected bundle.

    The kubelet refreshes a mounted ConfigMap in place, so a running process that re-reads the
    file picks up the new trust without a pod restart.
    """
    expected = set(bundle_fingerprints(expected_bundle_pem))
    assert expected, "the expected bundle holds no PEM certificate"
    assert pod_names, f"no pods to read {path} from"

    def synced() -> tuple[bool, str]:
        pending = [
            pod_name
            for pod_name in pod_names
            if set(bundle_fingerprints(read_pod_file(namespace, pod_name, container, path))) != expected
        ]
        return not pending, f"{path} not yet refreshed in {pending}"

    run_periodically(synced, timeout=timeout, sleep_time=10, msg=f"{path} in {container} to hold the published bundle")


def wait_for_om_mounted_appdb_bundle(ops_manager: MongoDBOpsManager, expected_bundle_pem: str, timeout: int = 600):
    """The Ops Manager JVM reads the AppDB CA from the fixed path of its mounted bundle ConfigMap."""
    wait_for_mounted_bundle(
        ops_manager.namespace,
        sorted(om_pod_states(ops_manager)),
        OM_CONTAINER,
        OM_APP_DB_CA_FILE_PATH,
        expected_bundle_pem,
        timeout,
    )


def wait_for_appdb_mounted_om_bundle(ops_manager: MongoDBOpsManager, expected_bundle_pem: str, timeout: int = 600):
    """The AppDB agents read the Ops Manager CA from -httpsCAFile, a file of the mounted bundle."""
    wait_for_mounted_bundle(
        ops_manager.namespace,
        sorted(appdb_pod_states(ops_manager)),
        AGENT_CONTAINER,
        AGENT_OM_CA_FILE_PATH,
        expected_bundle_pem,
        timeout,
    )


def wait_for_database_mounted_om_bundle(mdb: MongoDB, expected_bundle_pem: str, timeout: int = 600):
    """The downstream database agents read the Ops Manager CA from the mounted sslMMSCAConfigMap."""
    wait_for_mounted_bundle(
        mdb.namespace,
        sorted(pod_states(mdb.namespace, statefulset_pod_pattern(mdb.name))),
        AGENT_CONTAINER,
        AGENT_OM_CA_FILE_PATH,
        expected_bundle_pem,
        timeout,
    )


# --- Ops Manager HTTPS --------------------------------------------------------------


def served_certificate(url: str) -> str:
    """The leaf certificate served on the URL (PEM), without verifying it, or empty when unreachable."""
    parsed = urlparse(url)
    host = parsed.hostname
    if not host:
        return ""
    port = parsed.port or 443
    try:
        return ssl.get_server_certificate((host, port), timeout=30)
    except (OSError, socket.timeout, ssl.SSLError):
        return ""


def wait_for_om_serving_leaf_signed_by(
    ops_manager: MongoDBOpsManager, ca_pem: str, rejected_ca_pem: str = "", timeout: int = 1800
) -> str:
    """Waits until Ops Manager serves a leaf signed by ca_pem (and not by rejected_ca_pem)."""
    url = ops_manager.om_status().get_url()
    assert url, "Ops Manager URL is not reported"
    result: dict = {}

    def serving() -> bool:
        leaf = served_certificate(url)
        if not leaf or not cert_signed_by_bundle(leaf, ca_pem):
            return False
        if rejected_ca_pem and cert_signed_by_bundle(leaf, rejected_ca_pem):
            return False
        result["leaf"] = leaf
        return True

    run_periodically(serving, timeout=timeout, sleep_time=10, msg="Ops Manager to serve the leaf on the expected CA")
    return result["leaf"]


# --- Agents -------------------------------------------------------------------------


def _agent_last_seen(agent: dict) -> datetime | None:
    value = agent.get("lastConf") or agent.get("lastPing")
    if not value:
        return None
    try:
        return parse_time(str(value))
    except ValueError:
        return None


def wait_for_agents_checked_in_after(
    tester: OMTester,
    agent_type: str,
    since: datetime,
    hostnames: list[str],
    timeout: int = 900,
):
    """Waits until the agents on every given host fetched their configuration from Ops Manager after since.

    An agent only records a check-in once its HTTPS request to Ops Manager succeeded, so a recent
    lastConf proves the agent trusts the certificate Ops Manager currently serves.
    """
    assert hostnames, "no agent hostnames to check"

    def checked_in() -> tuple[bool, str]:
        agents = (
            tester.api_read_monitoring_agents() if agent_type == "MONITORING" else tester.api_read_automation_agents()
        )
        seen: dict[str, datetime] = {}
        for agent in agents:
            last_seen = _agent_last_seen(agent)
            hostname = agent.get("hostname", "")
            if last_seen is None or hostname not in hostnames:
                continue
            seen[hostname] = max(last_seen, seen.get(hostname, last_seen))
        stale = [hostname for hostname in hostnames if hostname not in seen or seen[hostname] <= since]
        return not stale, f"{agent_type} agents not checked in since {since.isoformat()}: {stale}"

    run_periodically(
        checked_in, timeout=timeout, sleep_time=10, msg=f"{agent_type} agents to check in with Ops Manager"
    )


def wait_for_appdb_agents_reach_om(ops_manager: MongoDBOpsManager, since: datetime, timeout: int = 900):
    """The AppDB monitoring agents keep reporting to Ops Manager over HTTPS after since."""
    tester = ops_manager.get_om_tester(ops_manager.app_db_name())
    wait_for_agents_checked_in_after(
        tester, "MONITORING", since, ops_manager.get_appdb_hostnames_for_monitoring(), timeout=timeout
    )


def database_hostnames(mdb: MongoDB) -> list[str]:
    """The FQDNs the agents of a single-cluster replica set register with in Ops Manager."""
    return [
        f"{mdb.name}-{index}.{mdb.get_service()}.{mdb.namespace}.svc.{mdb.get_cluster_domain()}"
        for index in range(mdb.get_members())
    ]


def wait_for_database_agents_reach_om(mdb: MongoDB, since: datetime, timeout: int = 900):
    """The downstream automation agents keep fetching their configuration and reach the goal state."""
    tester = mdb.get_om_tester()
    wait_for_agents_checked_in_after(tester, "AUTOMATION", since, database_hostnames(mdb), timeout=timeout)
    tester.wait_agents_ready(timeout=timeout)


# --- Downstream MongoDB -------------------------------------------------------------


def managed_om_replica_set(
    ops_manager: MongoDBOpsManager, name: str, namespace: str, custom_mdb_version: str, members: int = 1
) -> MongoDB:
    """A replica set managed by the Ops Manager, whose project trusts the managed Ops Manager CA bundle.

    The project ConfigMap names the operator-published <om>-managed-om-ca-bundle as sslMMSCAConfigMap,
    so the agents and the operator follow the bundle through a CA rotation. The bundle trusts only
    the self-signed Ops Manager CA, so an agent could not verify the public MongoDB download servers:
    the static architecture ships mongod in the image and needs no download.
    """
    names = ManagedCertificateNames.for_ops_manager(ops_manager)
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-for-om.yaml"), name=name, namespace=namespace).configure(
        ops_manager, name
    )
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["members"] = members
    resource["metadata"].setdefault("annotations", {})[STATIC_ARCHITECTURE_ANNOTATION] = "static"

    project_config_map = resource["spec"]["opsManager"]["configMapRef"]["name"]
    KubernetesTester.update_configmap(
        namespace, project_config_map, {"sslMMSCAConfigMap": names.om_ca_bundle_config_map}
    )
    try_load(resource)
    return resource


def wait_for_mongodb_running_at_current_generation(mdb: MongoDB, timeout: int = 1200):
    """Waits for Running at the current generation, so a cached Running phase of the previous spec doesn't count."""
    mdb.load()
    generation = mdb["metadata"]["generation"]

    def running() -> bool:
        mdb.load()
        return (
            mdb.get_status_phase() == Phase.Running
            and mdb.get_status_observed_generation() == generation
            and mdb["metadata"]["generation"] == generation
        )

    run_periodically(running, timeout=timeout, sleep_time=5, msg=f"{mdb.name} Running at generation {generation}")


# --- Operator -----------------------------------------------------------------------


def delete_operator_pods(operator: Operator, timeout: int = 600):
    """Deletes the operator pods and waits until a new operator pod is Running and ready."""
    core = kubernetes.client.CoreV1Api(api_client=operator.api_client)
    old_uids = set()
    for pod in operator.list_operator_pods():
        old_uids.add(pod.metadata.uid)
        core.delete_namespaced_pod(pod.metadata.name, operator.namespace)
    assert old_uids, "no operator pod to delete"

    def replaced() -> bool:
        pods = operator.list_operator_pods()
        if len(pods) != 1 or pods[0].metadata.uid in old_uids:
            return False
        status = pods[0].status
        if status is None or status.phase != "Running":
            return False
        statuses = status.container_statuses or []
        return bool(statuses) and all(container.ready for container in statuses)

    run_periodically(replaced, timeout=timeout, sleep_time=5, msg="a new operator pod to be ready")


# --- Certificates -------------------------------------------------------------------


def certificate_not_before(cert_pem: str) -> datetime:
    return x509.load_pem_x509_certificate(cert_pem.encode()).not_valid_before_utc


def certificate_issuing_condition(namespace: str, certificate_name: str) -> dict | None:
    status = read_certificate_status(namespace, certificate_name)
    return next(
        (
            condition
            for condition in status.get("conditions", [])
            if condition.get("type") == "Issuing" and condition.get("status") == "True"
        ),
        None,
    )


def wait_for_gated_leaf_reissue(
    namespace: str,
    certificate_name: str,
    previous_leaf: str,
    previous_revision: int,
    new_ca_pem: str,
    timeout: int = 1800,
) -> dict:
    """Waits for the leaf to be re-issued on the rotated CA and records how the operator triggered it.

    Returns the first new leaf seen, the Certificate revision once it moved past previous_revision,
    and the first Issuing=True condition with reason ManagedCARotated seen while waiting (None when
    the issuance completed between two polls).
    """
    result: dict = {"rotated_issuing": None}

    def reissued() -> bool:
        issuing = certificate_issuing_condition(namespace, certificate_name)
        if issuing and issuing.get("reason") == MANAGED_CA_ROTATED_REASON and result["rotated_issuing"] is None:
            result["rotated_issuing"] = issuing

        if "leaf" not in result:
            leaf = read_secret(namespace, certificate_name).get("tls.crt", "")
            if not leaf.strip() or leaf == previous_leaf or not cert_signed_by_bundle(leaf, new_ca_pem):
                return False
            result["leaf"] = leaf

        # cert-manager writes the Secret before it bumps the revision in the status.
        revision = read_certificate_status(namespace, certificate_name).get("revision", 0)
        if revision <= previous_revision:
            return False
        result["revision"] = revision
        return True

    run_periodically(
        reissued, timeout=timeout, sleep_time=3, msg=f"Certificate/{certificate_name} re-issued on the rotated CA"
    )
    return result


def wait_for_bundle_trusting(
    namespace: str,
    name: str,
    key: str,
    previous_latest_hash: str,
    included_pems: list[str],
    excluded_pems: list[str] | None = None,
    timeout: int = 900,
) -> dict:
    """Waits until a bundle ConfigMap moved past previous_latest_hash and trusts exactly what is expected.

    Every certificate of included_pems must be in the bundle and none of excluded_pems.
    """
    included = {fingerprint for pem in included_pems for fingerprint in bundle_fingerprints(pem)}
    excluded = {fingerprint for pem in excluded_pems or [] for fingerprint in bundle_fingerprints(pem)}
    assert included, "no CA to look for"
    result: dict = {}

    def trusting() -> tuple[bool, str]:
        snapshot = ca_bundle_snapshot(namespace, name, key)
        certificates = set(snapshot["certificates"])
        if not snapshot["latest_hash"] or snapshot["latest_hash"] == previous_latest_hash:
            return False, f"{name} latestHash is still {previous_latest_hash}"
        if not included.issubset(certificates) or excluded & certificates:
            return False, f"{name} holds {sorted(certificates)}"
        result.update(snapshot)
        return True, ""

    run_periodically(trusting, timeout=timeout, sleep_time=5, msg=f"CA bundle {name} to trust the expected CAs")
    return result


def generate_ca_pem(common_name: str) -> str:
    """A throwaway self-signed CA certificate (PEM) to feed additionalTrustedCAs."""
    key = ec.generate_private_key(ec.SECP256R1())
    subject = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, common_name)])
    now = now_utc()
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


# --- Prune annotation ---------------------------------------------------------------


def prune_annotation_value(ops_manager: MongoDBOpsManager) -> str | None:
    ops_manager.load()
    return (ops_manager["metadata"].get("annotations") or {}).get(PRUNE_PREVIOUS_CA_ANNOTATION)


def assert_prune_annotation_kept(ops_manager: MongoDBOpsManager, value: str, duration: int, check=None):
    """The annotation keeps its value for the whole duration, and the optional check keeps passing.

    The operator reconciles on the annotation change, so a value it would act on is gone well
    before the duration ends.
    """
    deadline = now_utc() + timedelta(seconds=duration)

    def elapsed() -> tuple[bool, str]:
        current = prune_annotation_value(ops_manager)
        assert current == value, f"the {PRUNE_PREVIOUS_CA_ANNOTATION}={value!r} annotation changed to {current!r}"
        if check is not None:
            check()
        return now_utc() >= deadline, f"{PRUNE_PREVIOUS_CA_ANNOTATION}={value!r} still set"

    run_periodically(elapsed, timeout=duration + 300, sleep_time=10, msg=f"{PRUNE_PREVIOUS_CA_ANNOTATION} to stay set")
