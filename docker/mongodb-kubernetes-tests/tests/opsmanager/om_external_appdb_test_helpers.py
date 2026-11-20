import copy
import socket
import ssl
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Optional

import pymongo
from cryptography import x509
from cryptography.hazmat.primitives import hashes
from kubernetes import client as k8s_client
from kubetester import read_configmap, read_secret, try_load
from kubetester.certs import Certificate
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pymongo.errors import AutoReconnect, ServerSelectionTimeoutError
from pytest import fixture
from tests.opsmanager.managedcert_helper import (
    APP_DB_CA_BUNDLE_KEY,
    ManagedCertificateNames,
    _write_appdb_ca,
    customer_ca_issuer,
)

SENTINEL_DOC = {"_id": "external-appdb-sentinel", "marker": "survived-migration"}
TEST_DB = "sentinelDb"
TEST_COLLECTION = "sentinelCollection"
META_OM_NAME = "meta-om"

# Default cert secret prefix used by create_appdb_certs (tests/common/cert/cert_issuer.py). Both the
# external AppDB MongoDB CR and the internal AppDB resolve their member cert secret to
# "<APPDB_CERT_PREFIX>-<appdb-name>-cert", so an adopted StatefulSet reuses the same certs.
APPDB_CERT_PREFIX = "appdb"

# Dedicated customer CA used by the managed-certificate external AppDB scenarios. The legacy global
# "ca-issuer" is backed by a static CA certificate without a keyUsage extension, which strict X.509
# verification rejects (error 92) for leaves it signs.
MANAGED_APPDB_CUSTOMER_CA = "managed-appdb-customer"

# The label the operator stamps on everything operator-managed certificates create. The cleanup on
# deletion finds the objects by it, so it has to name the owner of the graph.
MANAGED_CERTIFICATE_OWNER_UID_LABEL = "mongodb.com/v1.managedCertificateOwnerUID"


def appdb_tls_security(ca_configmap: str, cert_prefix: str = APPDB_CERT_PREFIX) -> dict:
    """Returns the spec.applicationDatabase.security block enabling TLS for the internal AppDB,
    mirroring fixtures/om_ops_manager_backup_tls.yaml."""
    return {"certsSecretPrefix": cert_prefix, "tls": {"ca": ca_configmap}}


def managed_appdb_security(issuer_name: Optional[str]) -> dict:
    """The spec security block for the managed-certificate external AppDB and its Primary OM."""
    server = {"issuerRef": {"name": issuer_name, "kind": "Issuer"}} if issuer_name else {}
    return {"managedCertificate": {"server": server}}


def appdb_cluster_domain(spec: dict) -> str:
    return spec.get("clusterDomain") or spec.get("clusterName") or "cluster.local"


def password_secret_name(om_name: str) -> str:
    return f"{om_name}-db-om-password"


def ref_kind_for_appdb() -> str:
    return "MongoDB"


def _assert_single_controller_owner_reference(metadata, kind: str, name: str):
    refs = metadata.owner_references or []
    assert len(refs) == 1, f"{metadata.name} must have exactly one ownerReference, got {refs}"
    assert refs[0].kind == kind
    assert refs[0].name == name
    assert refs[0].controller


def assert_owned_by_mongodb(metadata, name: str):
    """Asserts the resource is owned solely by the external AppDB MongoDB CR."""
    _assert_single_controller_owner_reference(metadata, ref_kind_for_appdb(), name)


def assert_owned_by_ops_manager(metadata, name: str):
    """Asserts the resource is owned solely by the MongoDBOpsManager resource."""
    _assert_single_controller_owner_reference(metadata, "MongoDBOpsManager", name)


def appdb_role_resource(namespace: str, custom_mdb_version: str, name: str) -> MongoDB:
    """Constructs the MongoDB(role: AppDB) CR that spec.externalApplicationDatabaseRef points at."""
    resource = MongoDB.from_yaml(yaml_fixture("om_external_appdb_db.yaml"), name=name, namespace=namespace)
    resource.set_version(custom_mdb_version)
    return resource


def meta_om_resource(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    """Builds the management-plane Ops Manager ("Meta OM")
    that owns the projects managing the External AppDB MongoDB CR. Deployment happens in each module's
    TestDeployMetaOpsManager class, not here."""
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_meta_om.yaml"),
        name=META_OM_NAME,
        namespace=namespace,
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    try_load(resource)

    return resource


def configure_appdb_role_mongodb(mdb: MongoDB, meta_om: MongoDBOpsManager, namespace: str) -> MongoDB:
    """Points the External AppDB CR's project/credentials at the Meta OM."""
    config_map_name = meta_om.get_or_create_mongodb_connection_config_map(mdb.name, f"{mdb.name}-project")

    mdb["spec"]["opsManager"]["configMapRef"]["name"] = config_map_name
    mdb["spec"]["credentials"] = meta_om.api_key_secret(namespace)

    return mdb


@fixture(scope="module")
def meta_om(
    default_operator: Operator,
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    """Deploys the Meta OM once per module and waits for it to be Running.

    default_operator is a dependency only: the operator has to exist before the Meta OM.
    """
    resource = meta_om_resource(namespace, custom_version, custom_appdb_version)
    resource.update()
    resource.om_status().assert_reaches_phase(Phase.Running, timeout=1800)
    return resource


@fixture(scope="module")
def issuer(cert_manager: str, namespace: str) -> str:
    """The customer-owned CA Issuer used by the managed-certificate scenarios.

    cert_manager is a dependency only: cert-manager has to be Ready before the Issuer.
    """
    return customer_ca_issuer(namespace, MANAGED_APPDB_CUSTOMER_CA)


def _sentinel_client(cnx_string: str, tls_ca_file: Optional[str]) -> pymongo.MongoClient:
    # a TLS AppDB connection string contains ssl=true, so pymongo needs the CA to verify the server.
    if tls_ca_file is not None:
        return pymongo.MongoClient(cnx_string, tlsCAFile=tls_ca_file, serverSelectionTimeoutMS=30000)
    return pymongo.MongoClient(cnx_string)


def write_sentinel_doc(cnx_string: str, tls_ca_file: Optional[str] = None):
    client = _sentinel_client(cnx_string, tls_ca_file)
    try:
        client[TEST_DB][TEST_COLLECTION].insert_one(dict(SENTINEL_DOC))
    finally:
        client.close()


def assert_sentinel_doc_present(cnx_string: str, tls_ca_file: Optional[str] = None, timeout: int = 0):
    """Asserts the sentinel document is present.

    With timeout > 0, retries on assertion and connection errors until the timeout — needed after
    a PIT restore, whose job reports FINISHED once OM prepared the files while the agents keep
    applying data afterwards.
    """
    start_time = time.time()
    while True:
        client = _sentinel_client(cnx_string, tls_ca_file)
        try:
            found = client[TEST_DB][TEST_COLLECTION].find_one({"_id": SENTINEL_DOC["_id"]})
            assert found is not None, "sentinel document did not survive the migration"
            assert found["marker"] == SENTINEL_DOC["marker"]
            return
        except (AssertionError, ServerSelectionTimeoutError, AutoReconnect) as e:
            if time.time() - start_time >= timeout:
                if timeout > 0:
                    raise AssertionError(f"sentinel document not found within {timeout}s; last error: {e}") from e
                raise
        finally:
            client.close()
        time.sleep(5)


def assert_project_exists(meta_om: MongoDBOpsManager, appdb_name: str):
    """Verifies the AppDB CR's project still exists on the Meta OM after reverse migration."""
    tester = meta_om.get_om_tester(project_name=f"{appdb_name}-project")
    tester.assert_group_exists()


def assert_no_migration_annotations(namespace: str, sts_name: str):
    sts = k8s_client.AppsV1Api().read_namespaced_stateful_set(sts_name, namespace)
    annotations = sts.metadata.annotations or {}
    assert "mongodb.com/appdb-migration-ready" not in annotations
    assert "mongodb.com/appdb-reverse-migration-ready" not in annotations


@dataclass
class ManagedAppDBScenario:
    primary: MongoDBOpsManager
    database: MongoDB
    meta: MongoDBOpsManager
    graph: dict = field(default_factory=dict)
    primary_pod_uids: set[str] = field(default_factory=set)
    uri: str = ""

    @property
    def namespace(self) -> str:
        return self.primary.namespace

    @property
    def names(self) -> ManagedCertificateNames:
        return ManagedCertificateNames.for_app_db(self.database.name)

    @property
    def leaf_name(self) -> str:
        return self.names.app_db_leaf_certificate

    @property
    def ca_name(self) -> str:
        return self.names.app_db_ca_bundle_config_map

    def write_ca(self, directory: Path) -> str:
        return _write_appdb_ca(self.namespace, directory / f"{self.database.name}-ca.pem", self.database.name)

    def wait_for_served_leaf(self, directory: Path, leaf: Optional[dict] = None) -> str:
        """Waits for every AppDB member to serve the current leaf, and returns the connection URI."""
        uri = self.primary.read_appdb_connection_url()
        material = leaf or read_secret(self.namespace, self.leaf_name)
        wait_for_managed_appdb_served_leaf(uri, self.write_ca(directory), material["tls.crt"])
        return uri

    def write_sentinel(self, directory: Path) -> None:
        write_sentinel_doc(self.primary.read_appdb_connection_url(), self.write_ca(directory))

    def assert_sentinel_present(self, directory: Path) -> None:
        assert_sentinel_doc_present(self.primary.read_appdb_connection_url(), self.write_ca(directory))

    def current_primary_pod_uids(self) -> set[str]:
        return {
            pod.metadata.uid
            for pod in k8s_client.CoreV1Api().list_namespaced_pod(self.namespace).items
            if any(
                ref.kind == "StatefulSet" and ref.name == self.primary.name
                for ref in pod.metadata.owner_references or []
            )
        }

    def renew_leaf(self, *, require_previous_hash: bool = False) -> dict:
        """Triggers a leaf renewal and returns the renewed leaf material once the derived PEM matches."""
        certificate = Certificate(name=self.leaf_name, namespace=self.namespace)
        old_leaf = read_secret(self.namespace, self.leaf_name)["tls.crt"]
        revision = certificate.request_renewal()
        renewed: dict = {}

        def is_renewed() -> bool:
            leaf = read_secret(self.namespace, self.leaf_name)
            if leaf["tls.crt"] == old_leaf:
                return False
            certificate.load()
            if certificate["status"].get("revision", 0) <= revision:
                return False
            pem = read_secret(self.namespace, self.names.app_db_pem_secret)
            if pem.get(pem.get("latestHash", "")) != leaf["tls.crt"] + leaf["tls.key"]:
                return False
            if require_previous_hash and not pem.get("previousHash"):
                return False
            renewed.update(leaf)
            return True

        KubernetesTester.wait_until(is_renewed, timeout=600, sleep_time=5, msg="managed AppDB leaf renewal")
        return renewed


def managed_appdb_graph(namespace: str, db_name: str) -> dict:
    """The cert-manager and TLS objects the external AppDB scenario owns, keyed by kind/name."""
    core = k8s_client.CoreV1Api()
    custom = k8s_client.CustomObjectsApi()
    serializer = k8s_client.ApiClient()
    managed_prefix = f"{db_name}-managed-"

    def serialized(obj, kind: str) -> dict:
        wire = serializer.sanitize_for_serialization(obj)
        wire["kind"] = kind
        return wire

    objects = [
        obj
        for plural in ("certificates", "issuers")
        for obj in custom.list_namespaced_custom_object("cert-manager.io", "v1", namespace, plural)["items"]
        if obj["metadata"]["name"].startswith(f"{db_name}-")
    ]
    fixed_secret_names = {
        f"{db_name}-server-ca",
        f"{db_name}-client-ca",
        f"{db_name}-om-password",
        f"{db_name}-keyfile",
    }
    objects.extend(
        serialized(obj, "Secret")
        for obj in core.list_namespaced_secret(namespace).items
        if obj.metadata.name.startswith(managed_prefix) or obj.metadata.name in fixed_secret_names
    )
    objects.extend(
        serialized(obj, "ConfigMap")
        for obj in core.list_namespaced_config_map(namespace).items
        if obj.metadata.name.startswith(managed_prefix)
    )
    return {
        f"{obj['kind']}/{obj['metadata']['name']}": {
            "uid": obj["metadata"]["uid"],
            "owners": obj["metadata"].get("ownerReferences") or [],
            "labels": obj["metadata"].get("labels") or {},
            "spec": obj.get("spec"),
            "type": obj.get("type"),
            "data": obj.get("data", {}),
        }
        for obj in objects
    }


def managed_certificate_owner_uid(spec: Optional[dict]) -> Optional[str]:
    """The owner UID label the operator asks cert-manager to put on the Secrets of a Certificate."""
    if not spec:
        return None
    secret_template = spec.get("secretTemplate") or {}
    return (secret_template.get("labels") or {}).get(MANAGED_CERTIFICATE_OWNER_UID_LABEL)


def without_managed_certificate_owner_uid(spec: Optional[dict]) -> Optional[dict]:
    """The Certificate spec without the owner UID label, so specs can be compared across a handover."""
    if not spec:
        return spec
    trimmed = copy.deepcopy(spec)
    secret_template = trimmed.get("secretTemplate") or {}
    labels = secret_template.get("labels")
    if labels is not None:
        labels.pop(MANAGED_CERTIFICATE_OWNER_UID_LABEL, None)
    return trimmed


def assert_managed_appdb_graph_owner(graph: dict, owner):
    """Asserts every managed object is owned by the expected AppDB CR or Primary OM."""
    kind, name, uid = owner.kind, owner.name, owner["metadata"]["uid"]
    certificate_uids = {entry["uid"] for key, entry in graph.items() if key.startswith("Certificate/")}
    for key, entry in graph.items():
        # The cleanup on deletion selects by this label, so it has to follow the same owner as the
        # owner references. It is set on the Certificate spec (cert-manager copies it onto the issued
        # secret) and directly on the secrets the operator writes itself.
        if key.startswith("Certificate/"):
            assert managed_certificate_owner_uid(entry["spec"]) == uid, f"{key} must carry the owner's UID label"
        label = (entry.get("labels") or {}).get(MANAGED_CERTIFICATE_OWNER_UID_LABEL)
        if label is not None:
            assert label == uid, f"{key} has the wrong owner UID label"

        owners = entry["owners"]
        # Derived PEMs and cert-manager TLS secret outputs follow the existing unowned convention.
        if key.startswith("Secret/") and not owners and (key.endswith("-pem") or entry["type"] == "kubernetes.io/tls"):
            continue
        if key.startswith("Secret/") and entry["type"] == "kubernetes.io/tls" and len(owners) == 1:
            if owners[0]["uid"] in certificate_uids:
                continue
        assert len(owners) == 1, f"{key} has unexpected owner references"
        assert owners[0]["kind"] == kind and owners[0]["name"] == name and owners[0]["uid"] == uid, key


def wait_for_managed_appdb_graph_owner(namespace: str, db_name: str, owner, timeout: int = 300) -> None:
    """Waits until cert-manager has propagated the handover's owner markers, then asserts them.

    The operator rewrites the Certificate specs synchronously, but cert-manager copies their
    secretTemplate labels onto the already-issued Secrets asynchronously. Right after the handover the
    issued Secrets can therefore still carry the previous owner's label for a moment.
    """
    deadline = time.time() + timeout
    while True:
        graph = managed_appdb_graph(namespace, db_name)
        try:
            assert_managed_appdb_graph_owner(graph, owner)
            return
        except AssertionError as exc:
            if time.time() >= deadline:
                raise AssertionError(f"managed AppDB graph ownership did not settle: {exc}") from exc
        time.sleep(5)


def assert_managed_appdb_chain(scenario: ManagedAppDBScenario):
    leaf = Certificate(name=scenario.leaf_name, namespace=scenario.namespace).load()
    assert any(c["type"] == "Ready" and c["status"] == "True" for c in leaf["status"]["conditions"])
    ca = read_configmap(scenario.namespace, scenario.ca_name)[APP_DB_CA_BUNDLE_KEY]
    material = read_secret(scenario.namespace, scenario.leaf_name)
    assert cert_signed_by_bundle(material["tls.crt"], ca)
    pem = read_secret(scenario.namespace, scenario.names.app_db_pem_secret)
    latest = pem["latestHash"]
    assert pem[latest] == material["tls.crt"] + material["tls.key"], "derived PEM does not match issued material"


def wait_for_managed_appdb_measurements(scenario: ManagedAppDBScenario):
    tester = scenario.meta.get_om_tester(project_name=f"{scenario.database.name}-project")
    project_id = tester.context.project_id

    def reports_connections(host_id: str) -> bool:
        return any(
            measurement["name"] == "CONNECTIONS"
            and any(point["value"] is not None for point in measurement["dataPoints"])
            for measurement in tester.api_read_measurements(host_id, project_id=project_id, period="PT1H")
        )

    def measured() -> bool:
        hosts = [h for h in tester.api_get_hosts()["results"] if h["hostname"].startswith(scenario.database.name + "-")]
        return len(hosts) == scenario.database["spec"]["members"] and all(
            reports_connections(host["id"]) for host in hosts
        )

    KubernetesTester.wait_until(measured, timeout=600, sleep_time=10, msg="external managed AppDB measurements")


def wait_for_managed_appdb_served_leaf(uri: str, ca_path: str, leaf_pem: str):
    expected = x509.load_pem_x509_certificate(leaf_pem.encode()).fingerprint(hashes.SHA256())
    context = ssl.create_default_context(cafile=ca_path)
    hosts = pymongo.uri_parser.parse_uri(uri)["nodelist"]
    last_observation = "no connection attempt completed"

    def fail(message: str):
        nonlocal last_observation
        last_observation = message
        return False, message

    def serving():
        for host, port in hosts:
            member = f"{host}:{port}"
            try:
                with socket.create_connection((host, port), timeout=5) as connection:
                    with context.wrap_socket(connection, server_hostname=host) as secure:
                        peer_der = secure.getpeercert(binary_form=True)
                        if peer_der is None:
                            return fail(f"{member} did not present a peer certificate")
                        actual = x509.load_der_x509_certificate(peer_der).fingerprint(hashes.SHA256())
                        if actual != expected:
                            return fail(f"{member} served SHA256 fingerprint {actual.hex()}, expected {expected.hex()}")
            except ssl.SSLCertVerificationError:
                # Strict verification rejected the chain or hostname. This is not transient:
                # surface the concrete error instead of letting the poll hide it behind the timeout.
                raise
            except OSError as exc:
                # Connection refused/reset/timeout during rollout or renewal: keep retrying, but
                # return the last observation so the timeout can report why it did not converge.
                return fail(f"{member} connection failed: {exc}")
        return True

    try:
        KubernetesTester.wait_until(
            serving,
            timeout=600,
            sleep_time=5,
            msg="managed leaf served by every AppDB member",
        )
    except AssertionError as exc:
        # run_periodically retains the last (False, reason) only as a span attribute; surface it
        # in the raised timeout so CI output explains the final observation.
        raise AssertionError(f"{exc}; last connection observation: {last_observation}") from exc
