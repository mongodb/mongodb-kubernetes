"""Migrating an Ops Manager's HTTPS and internal AppDB TLS to certificates signed by a different CA.

The deployment starts the legacy way, each component signed by its own customer CA. One edit then
points each component at an unrelated issuer and keeps the old CA trusted through
additionalTrustedCAs. The legacy ConfigMaps are not edited to include the new CA: the members that
have not restarted yet may reject the new leaf until they roll onto the widened bundle. That wait
can expire and report a Failed phase, as the MongoDB different-CA migration does, so the migration
wait ignores a transient Failed and still fails if the deployment never converges.
"""

import hashlib
import json
import socket
import ssl
import urllib.parse

import kubernetes
import pytest
from cryptography import x509
from cryptography.hazmat.primitives import serialization
from kubetester import create_or_update_configmap, read_configmap, read_secret, try_load
from kubetester.certs import create_ops_manager_tls_certs
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import fixture as load_fixture
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.common.cert.cert_issuer import create_appdb_certs
from tests.conftest import get_default_operator, get_operator_installation_config
from tests.opsmanager.managedcert_helper import (
    APP_DB_CA_BUNDLE_KEY,
    APP_DB_CERT_DIR,
    CA_BUNDLE_KEY,
    ManagedCertificateNames,
    _assert_appdb_connection_rejected,
    _assert_appdb_tls_scram,
    _assert_signed_by,
    _wait_for_appdb_automation_config,
    _wait_for_appdb_convergence,
    _wait_for_appdb_pem_secret,
    _write_appdb_ca,
    cert_manager,
    customer_ca_bundle,
    customer_ca_issuer,
    wait_for_certificates_ready,
    wait_for_om_https_trusted_by_bundle,
)
from tests.tls.managedcert_helper import RECOVERY_OFF_ENV_VAR, bundle_contains, latest_ca_bundle

OM_NAME = "om-managedcert-legacy-migration-new-ca"
APP_DB_NAME = f"{OM_NAME}-db"
OM_ISSUER = f"{OM_NAME}-om-customer-ca"
APP_DB_ISSUER = f"{OM_NAME}-appdb-customer-ca"
OM_OTHER_ISSUER = f"{OM_NAME}-om-other-ca"
APP_DB_OTHER_ISSUER = f"{OM_NAME}-appdb-other-ca"
OM_LEGACY_SECRET = f"{OM_NAME}-legacy-cert"
APP_DB_CERT_PREFIX = "appdb"
APP_DB_LEGACY_SECRET = f"{APP_DB_CERT_PREFIX}-{APP_DB_NAME}-cert"
OM_LEGACY_CA_CONFIG_MAP = f"{OM_NAME}-legacy-om-ca"
APP_DB_LEGACY_CA_CONFIG_MAP = f"{OM_NAME}-legacy-appdb-ca"

LAST_ACHIEVED_SPEC_ANNOTATION = "mongodb.com/v1.lastSuccessfulConfiguration"
OM_PEM_KEY_FILE_ENV_VAR = "OM_PROP_mms_https_PEMKeyFile"


def _data_digest(data: dict[str, str]) -> str:
    serialized = json.dumps(sorted(data.items()), separators=(",", ":"))
    return hashlib.sha256(serialized.encode()).hexdigest()


def _secret_state(namespace: str, name: str) -> tuple[str, str]:
    secret = kubernetes.client.CoreV1Api().read_namespaced_secret(name, namespace)
    assert secret.metadata.uid, f"{name} has no UID"
    return secret.metadata.uid, _data_digest(read_secret(namespace, name))


def _config_map_state(namespace: str, name: str) -> tuple[str, str]:
    config_map = kubernetes.client.CoreV1Api().read_namespaced_config_map(name, namespace)
    assert config_map.metadata.uid, f"{name} has no UID"
    return config_map.metadata.uid, _data_digest(read_configmap(namespace, name))


def _certificate_fingerprint(cert_pem: str) -> str:
    certificate = x509.load_pem_x509_certificate(cert_pem.encode())
    return hashlib.sha256(certificate.public_bytes(serialization.Encoding.DER)).hexdigest()


def _served_om_certificate_fingerprint(ops_manager: MongoDBOpsManager, ca_path: str) -> str:
    url = ops_manager.om_status().get_url()
    assert url, "Ops Manager URL is not reported"
    parsed = urllib.parse.urlsplit(url)
    hostname = parsed.hostname
    port = parsed.port
    assert hostname and port, f"Ops Manager URL is not a host:port URL: {url}"

    context = ssl.create_default_context(cafile=ca_path)
    with socket.create_connection((hostname, port), timeout=30) as connection:
        with context.wrap_socket(connection, server_hostname=hostname) as tls:
            der = tls.getpeercert(binary_form=True)
    assert der, "Ops Manager did not present a certificate"
    return hashlib.sha256(der).hexdigest()


def _value_digest(value: str) -> str:
    return hashlib.sha256(value.encode()).hexdigest()


def _last_achieved_spec(ops_manager: MongoDBOpsManager) -> dict:
    ops_manager.load()
    annotations = ops_manager["metadata"].get("annotations") or {}
    raw = annotations.get(LAST_ACHIEVED_SPEC_ANNOTATION, "")
    assert raw, f"{ops_manager.name} has no {LAST_ACHIEVED_SPEC_ANNOTATION} annotation"
    return json.loads(raw)


@fixture(scope="module")
def default_operator(cert_manager: str, namespace: str):
    config = get_operator_installation_config(namespace)
    config["security.managedCertificate.enabled"] = "true"
    config["customEnvVars"] = config["customEnvVars"] + RECOVERY_OFF_ENV_VAR
    return get_default_operator(namespace, config)


@fixture(scope="module")
def customer_cas(cert_manager: str, namespace: str) -> tuple[str, str, str, str]:
    """Two legacy roots and two unrelated target roots, none chained to another."""
    customer_ca_issuer(namespace, OM_ISSUER)
    customer_ca_issuer(namespace, APP_DB_ISSUER)
    customer_ca_issuer(namespace, OM_OTHER_ISSUER)
    customer_ca_issuer(namespace, APP_DB_OTHER_ISSUER)
    return OM_ISSUER, APP_DB_ISSUER, OM_OTHER_ISSUER, APP_DB_OTHER_ISSUER


@fixture(scope="module")
def legacy_ca_config_maps(namespace: str, customer_cas: tuple[str, str, str, str]) -> tuple[str, str]:
    create_or_update_configmap(
        namespace,
        OM_LEGACY_CA_CONFIG_MAP,
        {CA_BUNDLE_KEY: customer_ca_bundle(namespace, OM_ISSUER)},
    )
    create_or_update_configmap(
        namespace,
        APP_DB_LEGACY_CA_CONFIG_MAP,
        {APP_DB_CA_BUNDLE_KEY: customer_ca_bundle(namespace, APP_DB_ISSUER)},
    )
    return OM_LEGACY_CA_CONFIG_MAP, APP_DB_LEGACY_CA_CONFIG_MAP


@fixture(scope="module")
def legacy_certs(namespace: str, customer_cas: tuple[str, str, str, str]) -> None:
    create_ops_manager_tls_certs(OM_ISSUER, namespace, OM_NAME, OM_LEGACY_SECRET)
    create_appdb_certs(namespace, APP_DB_ISSUER, APP_DB_NAME, cert_prefix=APP_DB_CERT_PREFIX)


@fixture(scope="module")
def ops_manager(
    namespace: str,
    legacy_certs: None,
    legacy_ca_config_maps: tuple[str, str],
    custom_version: str,
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        load_fixture("om_appdb_scram.yaml"), name=OM_NAME, namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource["spec"]["security"] = {
        "tls": {"secretRef": {"name": OM_LEGACY_SECRET}, "ca": OM_LEGACY_CA_CONFIG_MAP},
    }
    resource["spec"]["applicationDatabase"]["security"] = {
        "certsSecretPrefix": APP_DB_CERT_PREFIX,
        "tls": {"ca": APP_DB_LEGACY_CA_CONFIG_MAP},
    }
    resource["spec"]["backup"] = {"enabled": False}
    try_load(resource)
    return resource


@fixture(scope="module")
def migration_state() -> dict:
    return {}


@fixture(scope="module", autouse=True)
def om_requests_ca(namespace: str, customer_cas: tuple[str, str, str, str], tmp_path_factory):
    """Trusts both OM CAs: the legacy endpoint and the endpoint after the issuer changes."""
    ca_file = tmp_path_factory.mktemp("om-cas") / "mms-ca.crt"
    ca_file.write_text(customer_ca_bundle(namespace, OM_ISSUER) + customer_ca_bundle(namespace, OM_OTHER_ISSUER))
    monkeypatch = pytest.MonkeyPatch()
    monkeypatch.setenv("REQUESTS_CA_BUNDLE", str(ca_file))
    try:
        yield str(ca_file)
    finally:
        monkeypatch.undo()


@pytest.mark.e2e_om_ops_manager_managedcert_legacy_migration_new_ca
class TestOpsManagerManagedCertificateLegacyMigrationNewCA:
    def test_install_operator(self, default_operator):
        default_operator.wait_for_operator_ready()

    def test_legacy_tls_running(
        self,
        ops_manager: MongoDBOpsManager,
        namespace: str,
        tmp_path,
        migration_state: dict,
    ):
        ops_manager.update()
        _wait_for_appdb_convergence(ops_manager, 3, timeout=1800)

        om_url = ops_manager.om_status().get_url()
        assert om_url is not None
        assert om_url.startswith("https://"), f"legacy Ops Manager must serve HTTPS, got {om_url}"

        om_ca = customer_ca_bundle(namespace, OM_ISSUER)
        app_db_ca = customer_ca_bundle(namespace, APP_DB_ISSUER)
        wait_for_om_https_trusted_by_bundle(ops_manager, om_ca)

        app_db_ca_path = tmp_path / "legacy-appdb-ca.pem"
        app_db_ca_path.write_text(app_db_ca)
        _assert_appdb_tls_scram(ops_manager, str(app_db_ca_path))
        _assert_appdb_connection_rejected(ops_manager.get_appdb_tester())

        legacy_pem = read_secret(namespace, f"{APP_DB_LEGACY_SECRET}-pem")
        legacy_pem_hash = legacy_pem.get("latestHash", "")
        assert legacy_pem_hash and legacy_pem_hash in legacy_pem

        migration_state.update(
            {
                "om_uid": ops_manager["metadata"]["uid"],
                "names": ManagedCertificateNames.for_ops_manager(ops_manager),
                "app_db_pem_hash": legacy_pem_hash,
                "app_db_pem_digest": _value_digest(legacy_pem[legacy_pem_hash]),
                "legacy_secret_state": _secret_state(namespace, OM_LEGACY_SECRET),
                "legacy_app_db_secret_state": _secret_state(namespace, APP_DB_LEGACY_SECRET),
                "legacy_om_ca_state": _config_map_state(namespace, OM_LEGACY_CA_CONFIG_MAP),
                "legacy_app_db_ca_state": _config_map_state(namespace, APP_DB_LEGACY_CA_CONFIG_MAP),
            }
        )

    def test_migrate_to_managed_certificates(self, ops_manager: MongoDBOpsManager, migration_state: dict):
        assert migration_state, "the legacy baseline must be captured before the migration"
        ops_manager.load()

        security = ops_manager["spec"]["security"]
        security["tls"]["secretRef"] = None
        security["tls"]["ca"] = None
        security["managedCertificate"] = {
            "issuerRef": {"name": OM_OTHER_ISSUER, "kind": "Issuer"},
            "additionalTrustedCAs": [{"name": OM_LEGACY_CA_CONFIG_MAP, "kind": "ConfigMap", "key": CA_BUNDLE_KEY}],
        }
        app_db_security = ops_manager["spec"]["applicationDatabase"]["security"]
        app_db_security["certsSecretPrefix"] = None
        app_db_security["tls"]["ca"] = None
        app_db_security["managedCertificate"] = {
            "server": {
                "issuerRef": {"name": APP_DB_OTHER_ISSUER, "kind": "Issuer"},
                "additionalTrustedCAs": [
                    {"name": APP_DB_LEGACY_CA_CONFIG_MAP, "kind": "ConfigMap", "key": APP_DB_CA_BUNDLE_KEY}
                ],
            }
        }
        ops_manager.update()

        # A member that has not restarted cannot validate the new leaf. Either component can report
        # Failed and recover; a migration that never reaches the current generation still fails the
        # timeout. The resource has no single phase, so each status is waited on separately.
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800, ignore_errors=True)
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800, ignore_errors=True)
        _wait_for_appdb_convergence(ops_manager, 3, timeout=1800)

    def test_managed_bundles_keep_the_legacy_cas_and_serve_the_new_leaves(
        self,
        ops_manager: MongoDBOpsManager,
        namespace: str,
        tmp_path,
        migration_state: dict,
    ):
        names: ManagedCertificateNames = migration_state["names"]
        _wait_for_appdb_convergence(ops_manager, 3, timeout=1800)
        wait_for_certificates_ready(ops_manager)

        legacy_om_ca = read_configmap(namespace, OM_LEGACY_CA_CONFIG_MAP)[CA_BUNDLE_KEY]
        legacy_app_db_ca = read_configmap(namespace, APP_DB_LEGACY_CA_CONFIG_MAP)[APP_DB_CA_BUNDLE_KEY]
        om_bundle = latest_ca_bundle(read_configmap(namespace, names.om_ca_bundle_config_map))
        app_db_bundle = latest_ca_bundle(read_configmap(namespace, names.app_db_ca_bundle_config_map))
        assert bundle_contains(om_bundle, legacy_om_ca), "the OM bundle must keep the legacy OM CA"
        assert bundle_contains(app_db_bundle, legacy_app_db_ca), "the AppDB bundle must keep the legacy AppDB CA"
        assert not bundle_contains(om_bundle, legacy_app_db_ca), "the OM bundle must not take the AppDB CA"
        assert not bundle_contains(app_db_bundle, legacy_om_ca), "the AppDB bundle must not take the OM CA"

        latest_hash = _wait_for_appdb_pem_secret(namespace, names.app_db_name)
        managed_pem = read_secret(namespace, names.app_db_pem_secret)
        legacy_hash = migration_state["app_db_pem_hash"]
        assert legacy_hash in managed_pem, f"{names.app_db_pem_secret} must keep the legacy file {legacy_hash}"
        assert _value_digest(managed_pem[legacy_hash]) == migration_state["app_db_pem_digest"]

        _wait_for_appdb_automation_config(ops_manager, 3, latest_hash)
        automation_config = ops_manager.get_automation_config_tester().automation_config
        for process in automation_config.get("processes") or []:
            if process.get("processType") != "mongod":
                continue
            key_file = (((process.get("args2_6") or {}).get("net") or {}).get("tls") or {}).get("certificateKeyFile")
            assert key_file == f"{APP_DB_CERT_DIR}/{latest_hash}", f"got {key_file}"

        new_om_ca = customer_ca_bundle(namespace, OM_OTHER_ISSUER)
        new_app_db_ca = customer_ca_bundle(namespace, APP_DB_OTHER_ISSUER)
        om_leaf = read_secret(namespace, names.om_leaf_certificate)["tls.crt"]
        app_db_leaf = read_secret(namespace, names.app_db_leaf_certificate)["tls.crt"]
        _assert_signed_by(om_leaf, new_om_ca, "the managed Ops Manager certificate")
        assert not cert_signed_by_bundle(om_leaf, legacy_om_ca), "the new OM leaf must not be signed by the legacy CA"
        assert not cert_signed_by_bundle(om_leaf, new_app_db_ca), "the AppDB issuer must not sign the OM leaf"
        _assert_signed_by(app_db_leaf, new_app_db_ca, "the managed AppDB certificate")
        assert not cert_signed_by_bundle(app_db_leaf, legacy_app_db_ca)
        assert not cert_signed_by_bundle(app_db_leaf, new_om_ca)

        om_ca_path = tmp_path / "om-other-ca.pem"
        om_ca_path.write_text(new_om_ca)
        wait_for_om_https_trusted_by_bundle(ops_manager, new_om_ca)
        assert _served_om_certificate_fingerprint(ops_manager, str(om_ca_path)) == _certificate_fingerprint(om_leaf)

        app_db_ca_path = _write_appdb_ca(namespace, tmp_path / "managed-appdb-ca.pem", names.app_db_name)
        _assert_appdb_tls_scram(ops_manager, app_db_ca_path)
        _assert_appdb_connection_rejected(ops_manager.get_appdb_tester())

        om_pem_latest_hash = read_secret(namespace, names.om_pem_secret)["latestHash"]
        om_container = next(
            container
            for container in ops_manager.read_statefulset().spec.template.spec.containers
            if container.name == "mongodb-ops-manager"
        )
        pem_env = next((env for env in om_container.env or [] if env.name == OM_PEM_KEY_FILE_ENV_VAR), None)
        assert pem_env is not None and pem_env.value.endswith(f"/{om_pem_latest_hash}")

    def test_legacy_resources_unchanged(self, ops_manager: MongoDBOpsManager, namespace: str, migration_state: dict):
        ops_manager.load()
        assert ops_manager["metadata"]["uid"] == migration_state["om_uid"]
        assert _secret_state(namespace, OM_LEGACY_SECRET) == migration_state["legacy_secret_state"]
        assert _secret_state(namespace, APP_DB_LEGACY_SECRET) == migration_state["legacy_app_db_secret_state"]
        assert _config_map_state(namespace, OM_LEGACY_CA_CONFIG_MAP) == migration_state["legacy_om_ca_state"]
        assert _config_map_state(namespace, APP_DB_LEGACY_CA_CONFIG_MAP) == migration_state["legacy_app_db_ca_state"]
        assert _certificate_fingerprint(
            read_secret(namespace, OM_LEGACY_SECRET)["tls.crt"]
        ) != _certificate_fingerprint(read_secret(namespace, migration_state["names"].om_leaf_certificate)["tls.crt"])
