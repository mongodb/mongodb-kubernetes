"""Shared fixtures and acceptance checks for the Ops Manager managed-certificate suites.

The four scenario modules deploy the same SCRAM AppDB and Ops Manager with operator-managed TLS.
They differ only in whether Ops Manager, the AppDB server category, or both use a test-owned CA
Issuer, so the resource factory and the acceptance checks live here and the modules keep the
scenario-specific setup.
"""

import re
import tempfile
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path

import kubernetes
import requests
from cryptography import x509
from cryptography.x509.oid import ExtendedKeyUsageOID
from kubernetes.client.rest import ApiException
from kubetester import list_matching_pods, read_configmap, read_secret
from kubetester.certs import Certificate, Issuer
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongotester import MongoTester
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.conftest import (
    get_central_cluster_client,
    get_central_cluster_name,
    install_cert_manager,
    wait_for_cert_manager_ready,
)
from tests.tls.managedcert_helper import (
    bundle_fingerprints,
    managed_certificate_operator,
    member_cert_issuance,
    pem_secret_hashes,
    read_certificate_status,
    trigger_certificate_renewal,
)

CERT_MANAGER_VERSION = "v1.18.2"

CA_BUNDLE_KEY = "mms-ca.crt"
APP_DB_CA_BUNDLE_KEY = "ca-pem"
APP_DB_CERT_DIR = "/var/lib/mongodb-automation/secrets/certs"
APP_DB_CA_DIR = "/var/lib/mongodb-automation/secrets/ca"
OM_APP_DB_CA_DIR = "/opt/mongodb/mms/ca/"
OM_APP_DB_CA_FILE_PATH = f"{OM_APP_DB_CA_DIR}{APP_DB_CA_BUNDLE_KEY}"
OM_APP_DB_CA_PROPERTY = "mongodb.ssl.CAFile"
OM_APP_DB_CA_ENV_VAR = f"OM_PROP_{OM_APP_DB_CA_PROPERTY.replace('.', '_')}"
APP_DB_CONTAINERS = ("mongodb-agent", "mongod")
OM_CONTAINER = "mongodb-ops-manager"

# Prefix of the AppDB warning the operator reports when managedCertificate.client is configured
# although SCRAM needs no client certificates.
UNUSED_APP_DB_CLIENT_WARNING = "spec.managedCertificate.client is set even though the deployment doesn't use x509"

# The customer CA root Certificate fields customer_ca_issuer configures and the checks re-verify.
CUSTOMER_CA_USAGES = ["digital signature", "cert sign", "crl sign"]


@dataclass(frozen=True)
class ManagedCertificateNames:
    """The cert-manager object names the operator derives from one Ops Manager resource."""

    om_name: str
    captured_app_db_name: str | None = None

    @classmethod
    def for_ops_manager(cls, ops_manager: MongoDBOpsManager) -> "ManagedCertificateNames":
        """Captures the AppDB name from the resource instead of re-deriving it."""
        accessor = getattr(ops_manager, "app_db_name", None)
        return cls(ops_manager.name, accessor() if callable(accessor) else None)

    @property
    def app_db_name(self) -> str:
        # Direct construction, and duck-typed callers without the accessor, keep the documented suffix.
        return self.captured_app_db_name or f"{self.om_name}-db"

    @property
    def om_self_signed_issuer(self) -> str:
        return f"{self.om_name}-managed-om-server-selfsigned"

    @property
    def om_ca_certificate(self) -> str:
        return f"{self.om_name}-managed-om-server-ca"

    @property
    def om_ca_issuer(self) -> str:
        return f"{self.om_name}-managed-om-server-ca-issuer"

    @property
    def om_leaf_certificate(self) -> str:
        return f"{self.om_name}-managed-om-cert"

    @property
    def om_pem_secret(self) -> str:
        return f"{self.om_leaf_certificate}-pem"

    @property
    def om_ca_bundle_config_map(self) -> str:
        return f"{self.om_name}-managed-om-ca-bundle"

    @property
    def app_db_self_signed_issuer(self) -> str:
        return f"{self.app_db_name}-server-selfsigned"

    @property
    def app_db_ca_certificate(self) -> str:
        return f"{self.app_db_name}-server-ca"

    @property
    def app_db_ca_issuer(self) -> str:
        return f"{self.app_db_name}-server-ca-issuer"

    @property
    def app_db_leaf_certificate(self) -> str:
        return f"{self.app_db_name}-managed-cert"

    @property
    def app_db_pem_secret(self) -> str:
        return f"{self.app_db_leaf_certificate}-pem"

    @property
    def app_db_ca_bundle_config_map(self) -> str:
        return f"{self.app_db_name}-managed-server-ca-bundle"

    @property
    def app_db_client_self_signed_issuer(self) -> str:
        return f"{self.app_db_name}-client-selfsigned"

    @property
    def app_db_client_ca_certificate(self) -> str:
        return f"{self.app_db_name}-client-ca"

    @property
    def app_db_client_ca_issuer(self) -> str:
        return f"{self.app_db_name}-client-ca-issuer"

    @property
    def app_db_client_ca_bundle_config_map(self) -> str:
        return f"{self.app_db_name}-managed-client-ca-bundle"

    @property
    def app_db_clusterfile_certificate(self) -> str:
        return f"{self.app_db_name}-managed-clusterfile"

    @property
    def app_db_agent_certificate(self) -> str:
        return f"{self.app_db_name}-managed-agent-certs"


def _ready_condition(resource) -> dict | None:
    conditions = resource.backing_obj.get("status", {}).get("conditions", [])
    return next((condition for condition in conditions if condition.get("type") == "Ready"), None)


def _wait_for_ready(resource, description: str, timeout: int = 300):
    def ready() -> bool:
        resource.load()
        condition = _ready_condition(resource)
        return condition is not None and condition.get("status") == "True"

    KubernetesTester.wait_until(ready, timeout=timeout, sleep_time=2, msg=f"{description} Ready condition")

    condition = _ready_condition(resource)
    assert condition is not None, f"{description} has no Ready condition"
    assert condition.get("status") == "True", f"{description} is not Ready: {condition}"


def _owner_references(resource) -> list[dict]:
    if isinstance(resource, dict):
        return (resource.get("metadata") or {}).get("ownerReferences") or []
    if hasattr(resource, "backing_obj"):
        return (resource.backing_obj.get("metadata") or {}).get("ownerReferences") or []
    metadata = kubernetes.client.ApiClient().sanitize_for_serialization(resource).get("metadata") or {}
    return metadata.get("ownerReferences") or []


def _assert_owned_by_ops_manager(resource, om_uid: str, description: str):
    owner_references = _owner_references(resource)
    assert len(owner_references) == 1, f"{description} must have exactly one owner reference, got {owner_references}"
    assert (
        owner_references[0].get("uid") == om_uid
    ), f"{description} is not owned by the Ops Manager UID {om_uid}: {owner_references}"


def _assert_user_owned(resource, description: str):
    owner_references = _owner_references(resource)
    assert not owner_references, f"{description} must stay owned by the test, got {owner_references}"


def _assert_absent(load, description: str):
    try:
        load()
    except ApiException as exception:
        assert exception.status == 404, f"{description} lookup failed with HTTP {exception.status}: {exception}"
        return
    raise AssertionError(f"{description} must not be created by the operator")


def _read_config_map_object(namespace: str, name: str):
    return kubernetes.client.CoreV1Api().read_namespaced_config_map(name, namespace)


def _read_secret_object(namespace: str, name: str):
    return kubernetes.client.CoreV1Api().read_namespaced_secret(name, namespace)


def _assert_current_certificates_ready(ops_manager: MongoDBOpsManager):
    ops_manager.load()
    generation = ops_manager["metadata"]["generation"]
    conditions = ops_manager["status"].get("conditions", [])
    condition = next((condition for condition in conditions if condition.get("type") == "CertificatesReady"), None)
    assert condition is not None, "Ops Manager has no CertificatesReady condition"
    assert condition.get("status") == "True", f"CertificatesReady is not True: {condition}"
    assert condition.get("observedGeneration") == generation, (
        f"CertificatesReady is for generation {condition.get('observedGeneration')}, "
        f"but the current generation is {generation}"
    )


def _configured_om_issuer(ops_manager: MongoDBOpsManager) -> str | None:
    security = ops_manager["spec"].get("security") or {}
    managed_certificate = security.get("managedCertificate") or {}
    return (managed_certificate.get("issuerRef") or {}).get("name")


def _configured_app_db_issuer(ops_manager: MongoDBOpsManager) -> str | None:
    app_db = ops_manager["spec"].get("applicationDatabase") or {}
    managed_certificate = (app_db.get("security") or {}).get("managedCertificate") or {}
    server = managed_certificate.get("server") or {}
    return (server.get("issuerRef") or {}).get("name")


def _configured_unused_client_issuer(ops_manager: MongoDBOpsManager) -> str | None:
    app_db = ops_manager["spec"].get("applicationDatabase") or {}
    managed_certificate = (app_db.get("security") or {}).get("managedCertificate") or {}
    client = managed_certificate.get("client") or {}
    return (client.get("issuerRef") or {}).get("name")


def _dns_sans(cert_pem: str) -> list[str]:
    certificate = x509.load_pem_x509_certificate(cert_pem.encode())
    san = certificate.extensions.get_extension_for_class(x509.SubjectAlternativeName)
    return san.value.get_values_for_type(x509.DNSName)


def _issued_ekus(cert_pem: str) -> set[x509.ObjectIdentifier]:
    certificate = x509.load_pem_x509_certificate(cert_pem.encode())
    return set(certificate.extensions.get_extension_for_class(x509.ExtendedKeyUsage).value)


def _assert_issued_ekus(cert_pem: str, expected: set[x509.ObjectIdentifier], description: str):
    try:
        usages = _issued_ekus(cert_pem)
    except x509.ExtensionNotFound:
        raise AssertionError(f"{description} has no extended key usage extension")

    missing = expected - usages
    assert not missing, (
        f"{description} is missing the issued EKUs {sorted(str(usage) for usage in missing)}: "
        f"{sorted(str(usage) for usage in usages)}"
    )


def _assert_signed_by(leaf_pem: str, ca_pem: str, description: str):
    assert cert_signed_by_bundle(leaf_pem, ca_pem), f"{description} must be signed by the expected CA"


def _appdb_cluster_domain(ops_manager: MongoDBOpsManager) -> str:
    spec = ops_manager["spec"]
    appdb_spec = spec.get("applicationDatabase") or {}
    return appdb_spec.get("clusterDomain") or spec.get("clusterDomain") or spec.get("clusterName") or "cluster.local"


def _appdb_sans(ops_manager: MongoDBOpsManager, namespace: str, members: int) -> list[str]:
    app_db_name = ops_manager.app_db_name()
    service_name = f"{app_db_name}-svc"
    cluster_domain = _appdb_cluster_domain(ops_manager)
    return sorted(
        f"{app_db_name}-{member}.{service_name}.{namespace}.svc.{cluster_domain}" for member in range(members)
    )


def _wait_for_appdb_convergence(ops_manager: MongoDBOpsManager, expected_members: int, timeout: int = 1800):
    """Waits for the current spec generation and the actual AppDB size, not a cached Running phase."""
    ops_manager.load()
    generation = ops_manager["metadata"]["generation"]

    def converged() -> bool:
        ops_manager.load()
        if ops_manager["metadata"]["generation"] != generation:
            return False

        status = ops_manager.get_status() or {}
        appdb = status.get("applicationDatabase") or {}
        om = status.get("opsManager") or {}
        if appdb.get("phase") != Phase.Running.name or appdb.get("observedGeneration") != generation:
            return False
        if om.get("phase") != Phase.Running.name or om.get("observedGeneration") != generation:
            return False
        if appdb.get("members") != expected_members:
            return False

        statefulset = ops_manager.read_appdb_statefulset()
        statefulset_status = statefulset.status
        return (
            statefulset.spec.replicas == expected_members
            and statefulset_status is not None
            and statefulset_status.observed_generation == statefulset.metadata.generation
            and statefulset_status.ready_replicas == expected_members
            and statefulset_status.current_replicas == expected_members
            and statefulset_status.replicas == expected_members
        )

    KubernetesTester.wait_until(
        converged,
        timeout=timeout,
        sleep_time=5,
        msg=f"AppDB convergence to {expected_members} members at the current generation",
    )


def _wait_for_appdb_leaf(
    namespace: str,
    app_db_name: str,
    expected_sans: list[str],
    minimum_revision: int | None = None,
    timeout: int = 600,
) -> tuple[int, str]:
    leaf_certificate = f"{app_db_name}-managed-cert"

    def leaf_is_current() -> bool:
        certificate = Certificate(name=leaf_certificate, namespace=namespace).load()
        revision = certificate["status"].get("revision", 0) if "status" in certificate else 0
        if minimum_revision is not None and revision < minimum_revision:
            return False

        leaf_pem = read_secret(namespace, leaf_certificate).get("tls.crt", "")
        return bool(leaf_pem.strip()) and sorted(_dns_sans(leaf_pem)) == expected_sans

    KubernetesTester.wait_until(
        leaf_is_current,
        timeout=timeout,
        sleep_time=5,
        msg=f"AppDB leaf certificate SANs for {len(expected_sans)} members",
    )
    return member_cert_issuance(app_db_name, namespace)


def _wait_for_appdb_pem_secret(namespace: str, app_db_name: str, timeout: int = 600) -> str:
    leaf_certificate = f"{app_db_name}-managed-cert"
    pem_secret = f"{leaf_certificate}-pem"

    def pem_is_current() -> bool:
        leaf = read_secret(namespace, leaf_certificate)
        pem = read_secret(namespace, pem_secret)
        latest_hash = pem.get("latestHash", "")
        return bool(latest_hash) and pem.get(latest_hash) == leaf.get("tls.crt", "") + leaf.get("tls.key", "")

    KubernetesTester.wait_until(
        pem_is_current,
        timeout=timeout,
        sleep_time=5,
        msg="AppDB derived PEM secret to match the issued leaf",
    )
    return read_secret(namespace, pem_secret)["latestHash"]


def _wait_for_appdb_automation_config(
    ops_manager: MongoDBOpsManager, expected_members: int, expected_leaf_hash: str, timeout: int = 900
):
    expected_key_file = f"{APP_DB_CERT_DIR}/{expected_leaf_hash}"
    app_db_name = ops_manager.app_db_name()
    bundle_cm_name = f"{app_db_name}-managed-server-ca-bundle"
    namespace = ops_manager.namespace

    def config_is_current() -> bool:
        automation_config = ops_manager.get_automation_config_tester().automation_config
        bundle = read_configmap(namespace, bundle_cm_name)
        expected_ca_path = f"{APP_DB_CA_DIR}/{bundle.get('latestHash', '')}"
        if (automation_config.get("tls") or {}).get("CAFilePath") != expected_ca_path:
            return False

        replica_sets = automation_config.get("replicaSets") or []
        if len(replica_sets) != 1 or replica_sets[0].get("_id") != app_db_name:
            return False
        if len(replica_sets[0].get("members") or []) != expected_members:
            return False

        processes = [
            process for process in automation_config.get("processes") or [] if process.get("processType") == "mongod"
        ]
        if len(processes) != expected_members:
            return False
        for process in processes:
            tls_args = ((process.get("args2_6") or {}).get("net") or {}).get("tls") or {}
            if tls_args.get("mode") != "requireTLS" or tls_args.get("certificateKeyFile") != expected_key_file:
                return False
        return True

    KubernetesTester.wait_until(
        config_is_current,
        timeout=timeout,
        sleep_time=5,
        msg=f"AppDB automation config for {expected_members} members with the managed certificate hash",
    )


def _write_appdb_ca(namespace: str, destination: Path, app_db_name: str) -> str:
    config_map = f"{app_db_name}-managed-server-ca-bundle"
    ca_pem = read_configmap(namespace, config_map).get(APP_DB_CA_BUNDLE_KEY, "")
    assert ca_pem.strip(), f"{config_map} has no {APP_DB_CA_BUNDLE_KEY}"
    destination.write_text(ca_pem)
    return str(destination)


def _assert_appdb_tls_scram(ops_manager: MongoDBOpsManager, ca_path: str):
    tester = ops_manager.get_appdb_tester(authenticate=True, ssl=True, ca_path=ca_path)
    try:
        # assert_connectivity performs an authenticated write when SCRAM credentials are configured.
        tester.assert_connectivity()
    finally:
        tester.client.close()


def _assert_appdb_connection_rejected(tester: MongoTester):
    try:
        tester.assert_no_connection()
    finally:
        tester.client.close()


def _container_by_name(statefulset, name: str):
    return next(
        (container for container in statefulset.spec.template.spec.containers or [] if container.name == name), None
    )


def _assert_volume_mount(statefulset, container_name: str, volume_name: str, mount_path: str):
    container = _container_by_name(statefulset, container_name)
    assert container is not None, f"container {container_name} not found"
    mount = next((mount for mount in container.volume_mounts or [] if mount.name == volume_name), None)
    assert mount is not None, f"{container_name} has no {volume_name} mount"
    assert (
        mount.mount_path == mount_path
    ), f"{container_name} mounts {volume_name} at {mount.mount_path}, expected {mount_path}"


def _assert_env_value(statefulset, container_name: str, env_name: str, expected: str):
    container = _container_by_name(statefulset, container_name)
    assert container is not None, f"container {container_name} not found"
    env = next((env for env in container.env or [] if env.name == env_name), None)
    assert env is not None, f"{container_name} has no {env_name} environment variable"
    assert env.value == expected, f"{container_name} sets {env_name} to {env.value}, expected {expected}"


def _issuer_ref(name: str) -> dict:
    return {"issuerRef": {"name": name, "kind": "Issuer"}}


def managed_ops_manager(
    name: str,
    namespace: str,
    custom_version: str,
    custom_appdb_version: str,
    om_issuer: str | None = None,
    appdb_issuer: str | None = None,
    unused_client_issuer: str | None = None,
) -> MongoDBOpsManager:
    """A SCRAM Ops Manager and AppDB with operator-managed TLS, signed by the given issuers."""
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_appdb_scram.yaml"), name=name, namespace=namespace)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    resource["spec"]["security"] = {"managedCertificate": _issuer_ref(om_issuer) if om_issuer else {}}

    app_db_managed_certificate = {}
    if appdb_issuer:
        app_db_managed_certificate["server"] = _issuer_ref(appdb_issuer)
    if unused_client_issuer:
        app_db_managed_certificate["client"] = _issuer_ref(unused_client_issuer)
    resource["spec"]["applicationDatabase"]["security"] = {"managedCertificate": app_db_managed_certificate}

    resource["spec"]["backup"] = {"enabled": False}
    resource.update()
    return resource


def customer_ca_issuer(namespace: str, name: str) -> str:
    """Creates a self-signed root CA and a CA Issuer on top of it, and returns the Issuer name.

    The resources belong to the test, not to Ops Manager: they have no owner reference and are
    never adopted, so they represent a customer-provided signer.
    """
    self_signed_issuer_name = f"{name}-selfsigned"
    ca_certificate_name = f"{name}-ca"

    self_signed_issuer = Issuer(name=self_signed_issuer_name, namespace=namespace)
    self_signed_issuer["spec"] = {"selfSigned": {}}
    self_signed_issuer.create()
    _wait_for_ready(self_signed_issuer, self_signed_issuer_name)

    ca_certificate = Certificate(name=ca_certificate_name, namespace=namespace)
    ca_certificate["spec"] = {
        "isCA": True,
        "commonName": name,
        "dnsNames": [name],
        "secretName": ca_certificate_name,
        "issuerRef": {"name": self_signed_issuer_name, "kind": "Issuer"},
        "usages": CUSTOMER_CA_USAGES,
    }
    ca_certificate.create()
    _wait_for_ready(ca_certificate, ca_certificate_name)

    ca_issuer = Issuer(name=name, namespace=namespace)
    ca_issuer["spec"] = {"ca": {"secretName": ca_certificate_name}}
    ca_issuer.create()
    _wait_for_ready(ca_issuer, name)

    return name


def customer_ca_bundle(namespace: str, issuer_name: str) -> str:
    """Returns the public certificate of a customer CA Issuer, resolved through its Secret reference."""
    issuer = Issuer(name=issuer_name, namespace=namespace).load()
    secret_name = (issuer["spec"].get("ca") or {}).get("secretName", "")
    assert secret_name, f"{issuer_name} is not a CA issuer: {issuer['spec']}"

    certificate = read_secret(namespace, secret_name).get("tls.crt", "")
    assert certificate.strip(), f"{secret_name} has no public certificate"
    return certificate


def _assert_customer_ca_secret_ownership(ca_secret, ca_certificate, ca_certificate_name: str, issuer_name: str):
    """The root Secret is test-owned: unowned, or owned only by its cert-manager Certificate."""
    owner_references = _owner_references(ca_secret)
    if not owner_references:
        return

    certificate_uid = (ca_certificate.backing_obj.get("metadata") or {}).get("uid")
    assert len(owner_references) == 1, (
        f"the {issuer_name} customer CA Secret must have no owner reference or exactly its "
        f"{ca_certificate_name} cert-manager owner, got {owner_references}"
    )
    owner_reference = owner_references[0]
    assert (
        owner_reference.get("kind") == "Certificate"
        and owner_reference.get("name") == ca_certificate_name
        and certificate_uid is not None
        and owner_reference.get("uid") == certificate_uid
    ), (
        f"the {issuer_name} customer CA Secret must not be adopted by {owner_reference}; "
        f"expected no owner or the {ca_certificate_name} Certificate with UID {certificate_uid}"
    )


def _assert_customer_ca(namespace: str, issuer_name: str):
    """The customer root is ready, user-owned and still configured the way the test created it."""
    self_signed_issuer_name = f"{issuer_name}-selfsigned"
    ca_certificate_name = f"{issuer_name}-ca"

    self_signed_issuer = Issuer(name=self_signed_issuer_name, namespace=namespace).load()
    ca_certificate = Certificate(name=ca_certificate_name, namespace=namespace).load()
    ca_issuer = Issuer(name=issuer_name, namespace=namespace).load()

    for resource, description in (
        (self_signed_issuer, self_signed_issuer_name),
        (ca_certificate, ca_certificate_name),
        (ca_issuer, issuer_name),
    ):
        _wait_for_ready(resource, description)
        _assert_user_owned(resource, description)

    assert "selfSigned" in self_signed_issuer["spec"]
    assert ca_certificate["spec"].get("isCA") is True

    common_name = ca_certificate["spec"].get("commonName")
    dns_names = ca_certificate["spec"].get("dnsNames")
    usages = ca_certificate["spec"].get("usages")
    assert common_name == issuer_name, f"{ca_certificate_name} commonName is {common_name!r}, expected {issuer_name!r}"
    assert dns_names == [issuer_name], f"{ca_certificate_name} dnsNames are {dns_names!r}, expected {[issuer_name]!r}"
    assert usages == CUSTOMER_CA_USAGES, f"{ca_certificate_name} usages are {usages!r}, expected {CUSTOMER_CA_USAGES!r}"

    assert ca_certificate["spec"].get("secretName") == ca_certificate_name
    issuer_ref = ca_certificate["spec"].get("issuerRef") or {}
    assert issuer_ref.get("name") == self_signed_issuer_name
    issuer_kind = issuer_ref.get("kind")
    assert issuer_kind == "Issuer", f"{ca_certificate_name} issuerRef kind is {issuer_kind!r}, expected 'Issuer'"
    assert ca_issuer["spec"]["ca"]["secretName"] == ca_certificate_name
    _assert_customer_ca_secret_ownership(
        _read_secret_object(namespace, ca_certificate_name),
        ca_certificate,
        ca_certificate_name,
        issuer_name,
    )


def _appdb_warnings(ops_manager: MongoDBOpsManager) -> list[str]:
    ops_manager.load()
    status = ops_manager.get_status() or {}
    return [str(warning) for warning in (status.get("applicationDatabase") or {}).get("warnings") or []]


def _wait_for_unused_appdb_client_warning(ops_manager: MongoDBOpsManager, timeout: int = 300, sleep_time: int = 2):
    def warning_is_reported() -> bool:
        return any(UNUSED_APP_DB_CLIENT_WARNING in warning for warning in _appdb_warnings(ops_manager))

    KubernetesTester.wait_until(
        warning_is_reported,
        timeout=timeout,
        sleep_time=sleep_time,
        msg="AppDB unused managedCertificate.client warning",
    )


def _assert_appdb_client_resources_absent(namespace: str, names: ManagedCertificateNames):
    """SCRAM must not get any managed client certificate, client trust bundle or client CA."""
    for certificate_name in (
        names.app_db_client_ca_certificate,
        names.app_db_clusterfile_certificate,
        names.app_db_agent_certificate,
    ):
        _assert_absent(
            lambda certificate_name=certificate_name: Certificate(name=certificate_name, namespace=namespace).load(),
            certificate_name,
        )
        _assert_absent(
            lambda certificate_name=certificate_name: _read_secret_object(namespace, certificate_name),
            certificate_name,
        )

    for issuer_name in (names.app_db_client_self_signed_issuer, names.app_db_client_ca_issuer):
        _assert_absent(
            lambda issuer_name=issuer_name: Issuer(name=issuer_name, namespace=namespace).load(), issuer_name
        )

    client_bundle = names.app_db_client_ca_bundle_config_map
    _assert_absent(lambda: _read_config_map_object(namespace, client_bundle), client_bundle)


@fixture(scope="module")
def cert_manager() -> str:
    cluster_client = get_central_cluster_client()
    result = install_cert_manager(
        cluster_client=cluster_client,
        cluster_name=get_central_cluster_name(),
        version=CERT_MANAGER_VERSION,
    )
    wait_for_cert_manager_ready(cluster_client=cluster_client)
    return result


@fixture(scope="module")
def default_operator(cert_manager: str, namespace: str) -> Operator:
    # The operator checks its cert-manager prerequisites once, at startup, so cert-manager must
    # already be Ready; cert_manager is a dependency, not used here.
    return managed_certificate_operator(namespace)


class ManagedOpsManagerCertificateTests:
    """The acceptance checks every managed-certificate scenario runs, without pytest marks."""

    def test_install_operator(self, default_operator: Operator):
        default_operator.wait_for_operator_ready()

    def test_ops_manager_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)

        om_url = ops_manager.om_status().get_url()
        assert om_url is not None
        assert om_url.startswith("https://")

    def test_appdb_monitoring_reaches_ops_manager(self, ops_manager: MongoDBOpsManager):
        ops_manager.assert_appdb_monitoring_group_was_created()
        ops_manager.assert_monitoring_data_exists()

    def test_operator_created_certificates_and_issuers(self, ops_manager: MongoDBOpsManager, namespace: str):
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        _assert_current_certificates_ready(ops_manager)
        om_uid = ops_manager["metadata"]["uid"]
        configured_issuer = _configured_om_issuer(ops_manager)

        leaf_certificate = Certificate(name=names.om_leaf_certificate, namespace=namespace).load()
        _wait_for_ready(leaf_certificate, names.om_leaf_certificate)
        _assert_owned_by_ops_manager(leaf_certificate, om_uid, names.om_leaf_certificate)

        if configured_issuer is None:
            ca_certificate = Certificate(name=names.om_ca_certificate, namespace=namespace).load()
            self_signed_issuer = Issuer(name=names.om_self_signed_issuer, namespace=namespace).load()
            ca_issuer = Issuer(name=names.om_ca_issuer, namespace=namespace).load()

            for resource, description in (
                (ca_certificate, names.om_ca_certificate),
                (self_signed_issuer, names.om_self_signed_issuer),
                (ca_issuer, names.om_ca_issuer),
            ):
                _wait_for_ready(resource, description)
                _assert_owned_by_ops_manager(resource, om_uid, description)

            assert ca_certificate["spec"]["secretName"] == names.om_ca_certificate
            assert ca_certificate["spec"].get("isCA") is True
            assert ca_certificate["spec"]["issuerRef"]["name"] == names.om_self_signed_issuer
            assert "selfSigned" in self_signed_issuer["spec"]
            assert ca_issuer["spec"]["ca"]["secretName"] == names.om_ca_certificate
            assert leaf_certificate["spec"]["issuerRef"]["name"] == names.om_ca_issuer

            ca_secret = read_secret(namespace, names.om_ca_certificate)
            assert ca_secret.get("tls.crt"), f"{names.om_ca_certificate} secret has no tls.crt"
            assert ca_secret.get("tls.key"), f"{names.om_ca_certificate} secret has no tls.key"
            expected_ca = ca_secret["tls.crt"]
        else:
            _assert_customer_ca(namespace, configured_issuer)
            assert leaf_certificate["spec"]["issuerRef"]["name"] == configured_issuer
            for resource, description in (
                (
                    lambda: Certificate(name=names.om_ca_certificate, namespace=namespace).load(),
                    names.om_ca_certificate,
                ),
                (
                    lambda: Issuer(name=names.om_self_signed_issuer, namespace=namespace).load(),
                    names.om_self_signed_issuer,
                ),
                (lambda: Issuer(name=names.om_ca_issuer, namespace=namespace).load(), names.om_ca_issuer),
            ):
                _assert_absent(resource, description)
            expected_ca = customer_ca_bundle(namespace, configured_issuer)

        assert leaf_certificate["spec"]["secretName"] == names.om_leaf_certificate

        tls_material = read_secret(namespace, names.om_leaf_certificate)
        assert tls_material.get("tls.crt"), f"{names.om_leaf_certificate} secret has no tls.crt"
        assert tls_material.get("tls.key"), f"{names.om_leaf_certificate} secret has no tls.key"

        ca_bundle = read_configmap(namespace, names.om_ca_bundle_config_map).get(CA_BUNDLE_KEY, "")
        assert ca_bundle.strip(), f"{names.om_ca_bundle_config_map} has no {CA_BUNDLE_KEY}"
        assert cert_signed_by_bundle(
            tls_material["tls.crt"], ca_bundle
        ), "the managed CA bundle must verify the Ops Manager server certificate"
        _assert_signed_by(tls_material["tls.crt"], expected_ca, "the Ops Manager server certificate")
        _assert_issued_ekus(
            tls_material["tls.crt"], {ExtendedKeyUsageOID.SERVER_AUTH}, "the Ops Manager server certificate"
        )

        _assert_owned_by_ops_manager(
            _read_config_map_object(namespace, names.om_ca_bundle_config_map),
            om_uid,
            names.om_ca_bundle_config_map,
        )
        _assert_owned_by_ops_manager(_read_secret_object(namespace, names.om_pem_secret), om_uid, names.om_pem_secret)

    def test_appdb_managed_certificates_and_issuers(self, ops_manager: MongoDBOpsManager, namespace: str):
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        _wait_for_appdb_convergence(ops_manager, 3)
        _assert_current_certificates_ready(ops_manager)
        om_uid = ops_manager["metadata"]["uid"]
        configured_issuer = _configured_app_db_issuer(ops_manager)

        leaf_certificate = Certificate(name=names.app_db_leaf_certificate, namespace=namespace).load()
        _wait_for_ready(leaf_certificate, names.app_db_leaf_certificate)
        _assert_owned_by_ops_manager(leaf_certificate, om_uid, names.app_db_leaf_certificate)

        if configured_issuer is None:
            ca_certificate = Certificate(name=names.app_db_ca_certificate, namespace=namespace).load()
            self_signed_issuer = Issuer(name=names.app_db_self_signed_issuer, namespace=namespace).load()
            ca_issuer = Issuer(name=names.app_db_ca_issuer, namespace=namespace).load()

            for resource, description in (
                (ca_certificate, names.app_db_ca_certificate),
                (self_signed_issuer, names.app_db_self_signed_issuer),
                (ca_issuer, names.app_db_ca_issuer),
            ):
                _wait_for_ready(resource, description)
                _assert_owned_by_ops_manager(resource, om_uid, description)

            assert ca_certificate["spec"]["secretName"] == names.app_db_ca_certificate
            assert ca_certificate["spec"].get("isCA") is True
            assert ca_certificate["spec"]["issuerRef"]["name"] == names.app_db_self_signed_issuer
            assert "selfSigned" in self_signed_issuer["spec"]
            assert ca_issuer["spec"]["ca"]["secretName"] == names.app_db_ca_certificate
            assert leaf_certificate["spec"]["issuerRef"]["name"] == names.app_db_ca_issuer

            app_db_ca_secret = read_secret(namespace, names.app_db_ca_certificate)
            assert app_db_ca_secret.get("tls.crt"), f"{names.app_db_ca_certificate} secret has no tls.crt"
            assert app_db_ca_secret.get("tls.key"), f"{names.app_db_ca_certificate} secret has no tls.key"
            expected_ca = app_db_ca_secret["tls.crt"]
        else:
            _assert_customer_ca(namespace, configured_issuer)
            assert leaf_certificate["spec"]["issuerRef"]["name"] == configured_issuer
            for resource, description in (
                (
                    lambda: Certificate(name=names.app_db_ca_certificate, namespace=namespace).load(),
                    names.app_db_ca_certificate,
                ),
                (
                    lambda: Issuer(name=names.app_db_self_signed_issuer, namespace=namespace).load(),
                    names.app_db_self_signed_issuer,
                ),
                (lambda: Issuer(name=names.app_db_ca_issuer, namespace=namespace).load(), names.app_db_ca_issuer),
            ):
                _assert_absent(resource, description)
            expected_ca = customer_ca_bundle(namespace, configured_issuer)

            om_issuer = _configured_om_issuer(ops_manager)
            if om_issuer is not None:
                assert (
                    customer_ca_bundle(namespace, om_issuer) != expected_ca
                ), "Ops Manager and AppDB must use distinct customer roots"

        assert leaf_certificate["spec"]["secretName"] == names.app_db_leaf_certificate
        assert "server auth" in leaf_certificate["spec"].get("usages", [])
        assert "client auth" in leaf_certificate["spec"].get("usages", [])

        revision, leaf_pem = _wait_for_appdb_leaf(namespace, names.app_db_name, _appdb_sans(ops_manager, namespace, 3))
        assert revision >= 1

        appdb_leaf_secret = read_secret(namespace, names.app_db_leaf_certificate)
        assert appdb_leaf_secret.get("tls.crt"), f"{names.app_db_leaf_certificate} secret has no tls.crt"
        assert appdb_leaf_secret.get("tls.key"), f"{names.app_db_leaf_certificate} secret has no tls.key"
        assert leaf_pem == appdb_leaf_secret["tls.crt"]

        appdb_ca_bundle = read_configmap(namespace, names.app_db_ca_bundle_config_map).get(APP_DB_CA_BUNDLE_KEY, "")
        assert appdb_ca_bundle.strip(), f"{names.app_db_ca_bundle_config_map} has no {APP_DB_CA_BUNDLE_KEY}"
        om_ca_bundle = read_configmap(namespace, names.om_ca_bundle_config_map).get(CA_BUNDLE_KEY, "")
        assert om_ca_bundle.strip(), f"{names.om_ca_bundle_config_map} has no {CA_BUNDLE_KEY}"
        assert appdb_ca_bundle.strip() != om_ca_bundle.strip(), "Ops Manager and AppDB must keep separate trust chains"
        assert cert_signed_by_bundle(
            leaf_pem, appdb_ca_bundle
        ), "the AppDB CA bundle must verify the AppDB server certificate"
        _assert_signed_by(leaf_pem, expected_ca, "the AppDB server certificate")
        assert not cert_signed_by_bundle(
            leaf_pem, om_ca_bundle
        ), "the independent Ops Manager CA must not verify the AppDB server certificate"
        _assert_issued_ekus(
            leaf_pem,
            {ExtendedKeyUsageOID.SERVER_AUTH, ExtendedKeyUsageOID.CLIENT_AUTH},
            "the AppDB member certificate",
        )

        _assert_owned_by_ops_manager(
            _read_config_map_object(namespace, names.app_db_ca_bundle_config_map),
            om_uid,
            names.app_db_ca_bundle_config_map,
        )

    def test_appdb_managed_tls_reaches_statefulsets_and_automation_config(
        self, ops_manager: MongoDBOpsManager, namespace: str
    ):
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        _wait_for_appdb_convergence(ops_manager, 3)
        latest_hash = _wait_for_appdb_pem_secret(namespace, names.app_db_name)
        _wait_for_appdb_automation_config(ops_manager, 3, latest_hash)

        appdb_statefulset = ops_manager.read_appdb_statefulset()
        appdb_volumes = appdb_statefulset.spec.template.spec.volumes or []
        pem_volume = next((volume for volume in appdb_volumes if volume.name == "secret-certs"), None)
        assert pem_volume is not None, "AppDB StatefulSet has no managed PEM volume"
        pem_secret_volume = pem_volume.secret
        assert pem_secret_volume is not None
        assert pem_secret_volume.secret_name == names.app_db_pem_secret

        ca_volume = next((volume for volume in appdb_volumes if volume.name == "secret-ca"), None)
        assert ca_volume is not None, "AppDB StatefulSet has no managed CA volume"
        ca_config_map_volume = ca_volume.config_map
        assert ca_config_map_volume is not None
        assert ca_config_map_volume.name == names.app_db_ca_bundle_config_map

        for container_name in APP_DB_CONTAINERS:
            _assert_volume_mount(appdb_statefulset, container_name, "secret-certs", APP_DB_CERT_DIR)
            _assert_volume_mount(appdb_statefulset, container_name, "secret-ca", APP_DB_CA_DIR)

        om_statefulset = ops_manager.read_statefulset()
        om_volumes = om_statefulset.spec.template.spec.volumes or []
        om_appdb_ca_volume = next((volume for volume in om_volumes if volume.name == "appdb-ca-certificate"), None)
        assert om_appdb_ca_volume is not None, "Ops Manager StatefulSet has no AppDB CA volume"
        om_appdb_ca_config_map = om_appdb_ca_volume.config_map
        assert om_appdb_ca_config_map is not None
        assert om_appdb_ca_config_map.name == names.app_db_ca_bundle_config_map
        assert om_appdb_ca_config_map.name != names.om_ca_bundle_config_map

        _assert_volume_mount(om_statefulset, OM_CONTAINER, "appdb-ca-certificate", OM_APP_DB_CA_DIR)
        _assert_env_value(om_statefulset, OM_CONTAINER, OM_APP_DB_CA_ENV_VAR, OM_APP_DB_CA_FILE_PATH)

        automation_config_tester = ops_manager.get_automation_config_tester()
        automation_config_tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256", active_auth_mechanism=False)
        assert automation_config_tester.automation_config["tls"]["clientCertificateMode"] == "OPTIONAL"

    def test_appdb_verified_tls_scram_and_rejected_connections(
        self, ops_manager: MongoDBOpsManager, namespace: str, tmp_path: Path
    ):
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        _wait_for_appdb_convergence(ops_manager, 3)
        _assert_appdb_tls_scram(ops_manager, _write_appdb_ca(namespace, tmp_path / "appdb-ca.pem", names.app_db_name))

        _assert_appdb_connection_rejected(ops_manager.get_appdb_tester())

        om_ca_bundle = read_configmap(namespace, names.om_ca_bundle_config_map).get(CA_BUNDLE_KEY, "")
        assert om_ca_bundle.strip(), f"{names.om_ca_bundle_config_map} has no {CA_BUNDLE_KEY}"
        wrong_ca_path = tmp_path / "ops-manager-ca.pem"
        wrong_ca_path.write_text(om_ca_bundle)
        _assert_appdb_connection_rejected(
            ops_manager.get_appdb_tester(authenticate=True, ssl=True, ca_path=str(wrong_ca_path))
        )

    def test_appdb_scale_up_and_down_with_managed_certificate(
        self, ops_manager: MongoDBOpsManager, namespace: str, tmp_path: Path
    ):
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        _wait_for_appdb_convergence(ops_manager, 3)
        _assert_current_certificates_ready(ops_manager)
        revision_before, leaf_before = member_cert_issuance(names.app_db_name, namespace)
        assert sorted(_dns_sans(leaf_before)) == _appdb_sans(ops_manager, namespace, 3)

        ops_manager.load()
        ops_manager["spec"]["applicationDatabase"]["members"] = 5
        ops_manager.update()

        _wait_for_appdb_convergence(ops_manager, 5)
        _assert_current_certificates_ready(ops_manager)
        revision_up, leaf_up = _wait_for_appdb_leaf(
            namespace,
            names.app_db_name,
            _appdb_sans(ops_manager, namespace, 5),
            minimum_revision=revision_before + 1,
        )
        assert revision_up == revision_before + 1, "the whole scale up must issue the AppDB leaf exactly once"
        assert leaf_up != leaf_before, "the AppDB leaf must cover the new members"
        pem_hash = _wait_for_appdb_pem_secret(namespace, names.app_db_name)
        _wait_for_appdb_automation_config(ops_manager, 5, pem_hash)
        _assert_appdb_tls_scram(ops_manager, _write_appdb_ca(namespace, tmp_path / "appdb-ca.pem", names.app_db_name))

        ops_manager.load()
        ops_manager["spec"]["applicationDatabase"]["members"] = 3
        ops_manager.update()

        _wait_for_appdb_convergence(ops_manager, 3)
        _assert_current_certificates_ready(ops_manager)
        revision_down, leaf_down = member_cert_issuance(names.app_db_name, namespace)
        assert revision_down == revision_up, "the AppDB leaf must not be reissued on scale down"
        assert leaf_down == leaf_up, "the AppDB leaf must not change or shrink on scale down"
        assert sorted(_dns_sans(leaf_down)) == _appdb_sans(ops_manager, namespace, 5)
        assert _wait_for_appdb_pem_secret(namespace, names.app_db_name) == pem_hash
        _wait_for_appdb_automation_config(ops_manager, 3, pem_hash)
        _assert_appdb_tls_scram(ops_manager, _write_appdb_ca(namespace, tmp_path / "appdb-ca.pem", names.app_db_name))


# --- Safe self-signed CA rotation -------------------------------------------------

# The operator annotates a published CA bundle ConfigMap with the time the current
# latestHash was published.
CA_BUNDLE_PUBLISHED_AT_ANNOTATION = "mongodb.com/ca-bundle-published-at"
# The prune annotation requests dropping a retained previous CA from the bundles.
PRUNE_PREVIOUS_CA_ANNOTATION = "mongodb.com/prune-previous-ca"
# Prefixes of the aggregated CertificatesReady condition message.
CERTIFICATES_READY_COMPONENTS = ("OpsManager", "AppDB", "Backup")
# The sentence the operator reports about a retained previous self-signed CA.
PREVIOUS_SERVER_CA_RETAINED = "Previous server CA retained in the trust bundle"
# The sentence when a prune was requested but could not be applied.
PREVIOUS_SERVER_CA_NOT_PRUNED = "The previous server CA was not pruned: issued certificates are still signed by it."


def _parse_rfc3339(value: str) -> datetime:
    """Parses an RFC3339 timestamp as Kubernetes writes it."""
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def pod_uids(namespace: str, name_prefix: str) -> dict[str, str]:
    """Pod names keyed by UID for the given prefix, used to detect pod recreation."""
    return {pod.metadata.name: pod.metadata.uid for pod in list_matching_pods(namespace, name_prefix=name_prefix)}


def ca_bundle_snapshot(namespace: str, name: str, key: str) -> dict:
    """The current state of a published CA bundle ConfigMap.

    Returns the latestHash, previousHash, certificate fingerprints and the published-at
    annotation.
    """
    config_map = _read_config_map_object(namespace, name)
    data = config_map.data or {}
    annotations = config_map.metadata.annotations or {}
    latest_hash = data.get("latestHash", "")
    bundle = data.get(key) or data.get(latest_hash, "")
    return {
        "latest_hash": latest_hash,
        "previous_hash": data.get("previousHash", ""),
        "certificates": bundle_fingerprints(bundle),
        "published_at": annotations.get(CA_BUNDLE_PUBLISHED_AT_ANNOTATION, ""),
    }


def wait_for_bundle_rotation(
    namespace: str,
    name: str,
    key: str,
    previous_latest_hash: str,
    previous_published_at: str = "",
    timeout: int = 900,
) -> dict:
    """Waits until a CA rotation widened a bundle ConfigMap.

    After the self-signed CA rotates the operator republishes the bundle with a new
    latestHash that holds both the new CA and the retained previous CA, sets previousHash
    and updates the published-at annotation.
    """
    result: dict = {}

    def rotated() -> bool:
        snapshot = ca_bundle_snapshot(namespace, name, key)
        if not snapshot["latest_hash"] or snapshot["latest_hash"] == previous_latest_hash:
            return False
        if snapshot["previous_hash"] != previous_latest_hash:
            return False
        if len(snapshot["certificates"]) < 2:
            return False
        if not snapshot["published_at"]:
            return False
        if previous_published_at and _parse_rfc3339(snapshot["published_at"]) <= _parse_rfc3339(previous_published_at):
            return False
        result.clear()
        result.update(snapshot)
        return True

    run_periodically(rotated, timeout=timeout, sleep_time=5, msg=f"CA bundle {name} widened after the CA rotation")
    return result


def wait_for_bundle_pruned(namespace: str, name: str, key: str, kept_ca_pem: str, timeout: int = 900) -> dict:
    """Waits until a bundle contains only the kept CA.

    kept_ca_pem is the PEM of the CA the bundle must keep (the current self-signed CA). The pruned
    bundle is a new bundle, so previousHash then names the widened bundle it replaced.
    """
    kept = set(bundle_fingerprints(kept_ca_pem))
    assert kept, "kept CA is not a valid PEM certificate"
    result: dict = {}

    def pruned() -> bool:
        snapshot = ca_bundle_snapshot(namespace, name, key)
        if set(snapshot["certificates"]) != kept:
            return False
        result.clear()
        result.update(snapshot)
        return True

    run_periodically(pruned, timeout=timeout, sleep_time=5, msg=f"CA bundle {name} pruned to the new CA only")
    return result


def wait_for_appdb_ca_file_path(ops_manager: MongoDBOpsManager, expected_hash: str, timeout: int = 900) -> dict:
    """Waits until the AppDB automation config CAFilePath ends with the bundle hash.

    A managed AppDB mounts its trust bundle at APP_DB_CA_DIR. The operator names the
    CA file after the bundle hash, so a bundle change moves the path and the agents
    restart mongod onto the new file.
    """
    expected = f"{APP_DB_CA_DIR}/{expected_hash}"
    result: dict = {}

    def updated() -> bool:
        automation_config = ops_manager.get_automation_config_tester().automation_config
        if (automation_config.get("tls") or {}).get("CAFilePath") != expected:
            return False
        for process in automation_config.get("processes") or []:
            if process.get("processType") != "mongod":
                continue
            tls_args = ((process.get("args2_6") or {}).get("net") or {}).get("tls") or {}
            ca_file = tls_args.get("CAFile")
            if ca_file is not None and ca_file != expected:
                return False
        result["automation_config"] = automation_config
        return True

    run_periodically(
        updated, timeout=timeout, sleep_time=5, msg="AppDB automation config CAFilePath to name the new CA hash"
    )
    return result["automation_config"]


def begin_ca_rotation(namespace: str, ca_certificate_name: str) -> str:
    """Triggers a reissue of a self-signed CA Certificate and returns the CA it replaced.

    The CA Certificate is issued with a new private key on every issuance (RotationPolicy Always).
    """
    previous_ca = read_secret(namespace, ca_certificate_name).get("tls.crt", "")
    assert previous_ca.strip(), f"{ca_certificate_name} has no tls.crt"
    trigger_certificate_renewal(namespace, ca_certificate_name)
    return previous_ca


def wait_for_ca_rotated(namespace: str, ca_certificate_name: str, previous_ca: str, timeout: int = 900) -> str:
    """Waits until the CA secret holds a different CA certificate and returns it."""
    result: dict = {}

    def rotated() -> bool:
        current = read_secret(namespace, ca_certificate_name).get("tls.crt", "")
        if not current.strip() or current == previous_ca:
            return False
        result["ca"] = current
        return True

    run_periodically(rotated, timeout=timeout, sleep_time=5, msg=f"CA Certificate/{ca_certificate_name} to rotate")
    return result["ca"]


def wait_for_leaf_reissued_on_new_ca(
    namespace: str,
    certificate_name: str,
    pem_secret_name: str,
    previous_leaf: str,
    previous_pem_hash: str,
    new_ca_pem: str,
    timeout: int = 1800,
) -> tuple[str, str]:
    """Waits until a leaf Certificate is reissued on the rotated CA and its files renewed.

    Returns the leaf PEM and the new PEM secret hash.
    """
    result: dict = {}

    def reissued() -> bool:
        leaf = read_secret(namespace, certificate_name).get("tls.crt", "")
        latest_hash, _ = pem_secret_hashes(namespace, pem_secret_name)
        if not leaf.strip() or leaf == previous_leaf:
            return False
        if not latest_hash or latest_hash == previous_pem_hash:
            return False
        if not cert_signed_by_bundle(leaf, new_ca_pem):
            return False
        result["leaf"] = leaf
        result["pem_hash"] = latest_hash
        return True

    run_periodically(
        reissued,
        timeout=timeout,
        sleep_time=5,
        msg=f"Certificate/{certificate_name} reissued on the rotated CA",
    )
    return result["leaf"], result["pem_hash"]


def _component_message(message: str, component: str) -> str:
    """The segment of an aggregated CertificatesReady message for one component."""
    match = re.search(
        rf"(?:^|; ){re.escape(component)}: (.*?)(?=; (?:{'|'.join(CERTIFICATES_READY_COMPONENTS)}): |$)",
        message,
        re.DOTALL,
    )
    return match.group(1) if match else ""


def _certificates_ready_condition(ops_manager: MongoDBOpsManager) -> dict | None:
    """The current CertificatesReady condition (the resource is reloaded), or None when not set."""
    ops_manager.load()
    for condition in (ops_manager.get_status() or {}).get("conditions") or []:
        if condition.get("type") == "CertificatesReady":
            return condition
    return None


def certificates_ready_message(ops_manager: MongoDBOpsManager) -> str:
    """The aggregated CertificatesReady message, or empty when not set."""
    condition = _certificates_ready_condition(ops_manager)
    return condition.get("message", "") if condition else ""


def component_certificates_ready_message(ops_manager: MongoDBOpsManager, component: str) -> str:
    """The CertificatesReady sentence of one component in the aggregated message."""
    return _component_message(certificates_ready_message(ops_manager), component)


def wait_for_component_certificates_ready_message(
    ops_manager: MongoDBOpsManager,
    component: str,
    expected: str,
    *,
    present: bool = True,
    timeout: int = 900,
) -> str:
    """Waits until a component's CertificatesReady sentence contains (or drops) expected text."""
    result: dict = {}

    def matches() -> bool:
        condition = _certificates_ready_condition(ops_manager)
        if condition is None or condition.get("status") != "True":
            return False
        component_message = _component_message(condition.get("message", ""), component)
        if not component_message:
            return False
        return (expected in component_message) == present

    msg = f"{component} CertificatesReady message {'to contain' if present else 'to drop'} {expected!r}"
    run_periodically(matches, timeout=timeout, sleep_time=5, msg=msg)

    condition = _certificates_ready_condition(ops_manager)
    return _component_message(condition.get("message", ""), component) if condition else ""


def wait_for_certificates_ready(ops_manager: MongoDBOpsManager, timeout: int = 900):
    """Waits until CertificatesReady is True for the current generation."""

    def ready() -> bool:
        ops_manager.load()
        condition = _certificates_ready_condition(ops_manager)
        return (
            condition is not None
            and condition.get("status") == "True"
            and condition.get("observedGeneration") == ops_manager["metadata"]["generation"]
        )

    run_periodically(ready, timeout=timeout, sleep_time=5, msg=f"{ops_manager.name} CertificatesReady=True")


def request_prune_previous_ca(ops_manager: MongoDBOpsManager, value: str):
    """Annotates the resource so the operator drops the retained previous CA(s).

    value is "om", "appdb", "backup", or a comma-separated combination, or "true" for all.
    """
    ops_manager.load()
    ops_manager["metadata"].setdefault("annotations", {})[PRUNE_PREVIOUS_CA_ANNOTATION] = value
    ops_manager.update()


def wait_for_prune_annotation_removed(ops_manager: MongoDBOpsManager, timeout: int = 900):
    """Waits until the operator has fulfilled the prune annotation and removed it."""

    def removed() -> bool:
        ops_manager.load()
        annotations = ops_manager["metadata"].get("annotations") or {}
        return PRUNE_PREVIOUS_CA_ANNOTATION not in annotations

    run_periodically(
        removed,
        timeout=timeout,
        sleep_time=5,
        msg=f"mongodb.com/prune-previous-ca annotation removed from {ops_manager.name}",
    )


def read_component_certificate_renewal(ops_manager: MongoDBOpsManager, status_field: str) -> dict | None:
    """Returns status.<field>.certificateRenewals.server, or None when not reported."""
    ops_manager.load()
    status = ops_manager.get_status() or {}
    component = status.get(status_field) or {}
    return (component.get("certificateRenewals") or {}).get("server")


def wait_for_component_certificate_renewal(
    ops_manager: MongoDBOpsManager,
    status_field: str,
    certificate_name: str,
    timeout: int = 600,
) -> dict:
    """Waits until the component's renewal status names the expected certificate."""
    result: dict = {}

    def reported() -> bool:
        renewal = read_component_certificate_renewal(ops_manager, status_field)
        if not renewal or renewal.get("name") != certificate_name:
            return False
        if not renewal.get("notAfter") or not renewal.get("renewalTime"):
            return False
        result["renewal"] = renewal
        return True

    run_periodically(
        reported, timeout=timeout, sleep_time=5, msg=f"{status_field}.certificateRenewals={certificate_name}"
    )
    return result["renewal"]


def latest_ca_bundle_pem(namespace: str, config_map_name: str, key: str) -> str:
    """The current CA bundle from the data of a bundle ConfigMap under the fixed alias key."""
    data = _read_config_map_object(namespace, config_map_name).data or {}
    return (data.get(key) or "").strip()


def wait_for_om_https_trusted_by_bundle(ops_manager: MongoDBOpsManager, bundle_pem: str, timeout: int = 900):
    """Waits until the Ops Manager health endpoint serves a certificate the bundle trusts.

    The TLS handshake validates the Ops Manager server certificate against the bundle.
    """
    url = ops_manager.om_status().get_url()
    assert url, "Ops Manager URL is not reported"

    def reachable() -> bool:
        with tempfile.NamedTemporaryFile(mode="w", suffix=".pem") as ca_file:
            ca_file.write(bundle_pem)
            ca_file.flush()
            try:
                response = requests.get(f"{url.rstrip('/')}/monitor/health", verify=ca_file.name, timeout=30)
            except requests.exceptions.RequestException:
                return False
        return response.status_code == 200

    run_periodically(reachable, timeout=timeout, sleep_time=5, msg="Ops Manager HTTPS trusted by its CA bundle")
