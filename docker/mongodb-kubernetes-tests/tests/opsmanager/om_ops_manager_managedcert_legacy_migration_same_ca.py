"""Migrating an Ops Manager's HTTPS and internal AppDB TLS to operator-managed certificates signed by the same CA.

The deployment starts the legacy way: Ops Manager serves HTTPS from a user-created Secret
(<name>-legacy-cert) signed by a customer CA Issuer, and the single-cluster AppDB uses
certsSecretPrefix, its user-created <prefix>-<appdb>-cert Secret, and a customer CA ConfigMap.

It is then migrated in one edit: the OM tls.secretRef, the AppDB certsSecretPrefix and both tls.ca
references are removed and managedCertificate is added, pointing each component at the same customer
CA Issuer that already signed its legacy certificate. Each component keeps the CA it already trusts,
so the members keep trusting each other while they restart. Automatic recovery is turned off, because
it would rescue a stuck migration after 20 minutes and hide the problem: both components have to
converge on their own.

The AppDB StatefulSet starts reading the managed -pem Secret before the automation config is
updated, so the operator seeds that Secret with the certificate the old automation config names.
This test asserts that the old file is still in the managed PEM Secret, next to the new one.
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
from kubetester import create_or_update_configmap, list_matching_pods, read_configmap, read_secret, try_load
from kubetester.certs import create_ops_manager_tls_certs
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import fixture as load_fixture
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture
from tests.common.cert.cert_issuer import create_appdb_certs
from tests.conftest import get_default_operator, get_member_cluster_api_client, get_operator_installation_config
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
from tests.tls.managedcert_helper import RECOVERY_OFF_ENV_VAR

OM_NAME = "om-managedcert-legacy-migration"
APP_DB_NAME = f"{OM_NAME}-db"
OM_ISSUER = f"{OM_NAME}-om-customer-ca"
APP_DB_ISSUER = f"{OM_NAME}-appdb-customer-ca"
OM_LEGACY_SECRET = f"{OM_NAME}-legacy-cert"
APP_DB_CERT_PREFIX = "appdb"
APP_DB_LEGACY_SECRET = f"{APP_DB_CERT_PREFIX}-{APP_DB_NAME}-cert"
OM_LEGACY_CA_CONFIG_MAP = f"{OM_NAME}-legacy-om-ca"
APP_DB_LEGACY_CA_CONFIG_MAP = f"{OM_NAME}-legacy-appdb-ca"

# The annotation the operator writes with the last spec it applied successfully. It is the
# migration baseline: the operator reads the legacy TLS blocks from it.
LAST_ACHIEVED_SPEC_ANNOTATION = "mongodb.com/v1.lastSuccessfulConfiguration"

# The environment variable the Ops Manager container gets for its HTTPS PEM file.
OM_PEM_KEY_FILE_ENV_VAR = "OM_PROP_mms_https_PEMKeyFile"


def _data_digest(data: dict[str, str]) -> str:
    """A stable digest of a Secret's or ConfigMap's keys and values, without printing them."""
    serialized = json.dumps(sorted(data.items()), separators=(",", ":"))
    return hashlib.sha256(serialized.encode()).hexdigest()


def _secret_state(namespace: str, name: str) -> tuple[str, str]:
    """The UID and the content digest of a Secret."""
    secret = kubernetes.client.CoreV1Api().read_namespaced_secret(name, namespace)
    assert secret.metadata.uid, f"{name} has no UID"
    return secret.metadata.uid, _data_digest(read_secret(namespace, name))


def _config_map_state(namespace: str, name: str) -> tuple[str, str]:
    """The UID and the content digest of a ConfigMap."""
    config_map = kubernetes.client.CoreV1Api().read_namespaced_config_map(name, namespace)
    assert config_map.metadata.uid, f"{name} has no UID"
    return config_map.metadata.uid, _data_digest(read_configmap(namespace, name))


def _certificate_fingerprint(cert_pem: str) -> str:
    """The SHA-256 of a PEM certificate's DER encoding."""
    certificate = x509.load_pem_x509_certificate(cert_pem.encode())
    return hashlib.sha256(certificate.public_bytes(serialization.Encoding.DER)).hexdigest()


def _served_om_certificate_fingerprint(ops_manager: MongoDBOpsManager, ca_path: str) -> str:
    """The SHA-256 of the certificate the Ops Manager HTTPS endpoint actually serves.

    The handshake validates the served certificate against ca_path, so an untrusted or
    hostname-mismatched certificate raises instead of returning a fingerprint.
    """
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


def _last_achieved_spec(ops_manager: MongoDBOpsManager) -> dict:
    ops_manager.load()
    annotations = ops_manager["metadata"].get("annotations") or {}
    raw = annotations.get(LAST_ACHIEVED_SPEC_ANNOTATION, "")
    assert raw, f"{ops_manager.name} has no {LAST_ACHIEVED_SPEC_ANNOTATION} annotation"
    return json.loads(raw)


@fixture(scope="module")
def default_operator(cert_manager: str, namespace: str):
    # cert-manager has to be installed before the operator, which decides once, at startup, whether
    # it may use cert-manager. Automatic recovery is turned off, see the module docstring.
    config = get_operator_installation_config(namespace)
    config["security.managedCertificate.enabled"] = "true"
    config["customEnvVars"] = config["customEnvVars"] + RECOVERY_OFF_ENV_VAR
    return get_default_operator(namespace, config)


@fixture(scope="module")
def customer_cas(cert_manager: str, namespace: str) -> tuple[str, str]:
    """Two independent customer roots, one per component, kept for the whole migration.

    cert-manager has to be installed first: these are cert-manager Issuers and Certificates.
    """
    customer_ca_issuer(namespace, OM_ISSUER)
    customer_ca_issuer(namespace, APP_DB_ISSUER)
    return OM_ISSUER, APP_DB_ISSUER


@fixture(scope="module")
def legacy_ca_config_maps(namespace: str, customer_cas: tuple[str, str]) -> tuple[str, str]:
    """User-owned CA ConfigMaps, one per component, as a legacy deployment provides them."""
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
def legacy_certs(namespace: str, customer_cas: tuple[str, str]) -> None:
    """The leaf certificates a customer created before the migration, signed by their own CAs."""
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
    """Baseline values captured from the legacy deployment, filled in by the baseline test."""
    return {}


@fixture(scope="module", autouse=True)
def om_requests_ca(namespace: str, customer_cas: tuple[str, str], tmp_path_factory):
    """Trusts the customer OM CA for the requests the helper functions make to Ops Manager.

    Ops Manager keeps the same customer signer across the migration, so one bundle verifies the
    legacy and the migrated endpoint.
    """
    ca_file = tmp_path_factory.mktemp("om-customer-ca") / "mms-ca.crt"
    ca_file.write_text(customer_ca_bundle(namespace, OM_ISSUER))
    monkeypatch = pytest.MonkeyPatch()
    monkeypatch.setenv("REQUESTS_CA_BUNDLE", str(ca_file))
    try:
        yield str(ca_file)
    finally:
        monkeypatch.undo()


@pytest.mark.e2e_om_ops_manager_managedcert_legacy_migration_same_ca
class TestOpsManagerManagedCertificateLegacyMigrationSameCA:
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
        assert om_ca.strip() and app_db_ca.strip(), "both customer CAs must be readable"

        wait_for_om_https_trusted_by_bundle(ops_manager, om_ca)

        app_db_ca_path = tmp_path / "legacy-appdb-ca.pem"
        app_db_ca_path.write_text(app_db_ca)
        _assert_appdb_tls_scram(ops_manager, str(app_db_ca_path))
        _assert_appdb_connection_rejected(ops_manager.get_appdb_tester())

        ops_manager.assert_appdb_monitoring_group_was_created()
        ops_manager.assert_monitoring_data_exists()

        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        last_achieved = _last_achieved_spec(ops_manager)
        assert (last_achieved.get("security") or {}).get("tls", {}).get("secretRef", {}).get("name") == OM_LEGACY_SECRET
        assert "managedCertificate" not in (last_achieved.get("security") or {})
        assert (last_achieved.get("applicationDatabase") or {}).get("security", {}).get(
            "certsSecretPrefix"
        ) == APP_DB_CERT_PREFIX
        assert "managedCertificate" not in (last_achieved.get("applicationDatabase") or {}).get("security", {})

        om_leaf = read_secret(namespace, OM_LEGACY_SECRET)["tls.crt"]
        app_db_leaf = read_secret(namespace, APP_DB_LEGACY_SECRET)["tls.crt"]
        _assert_signed_by(om_leaf, om_ca, "the legacy Ops Manager certificate")
        assert not cert_signed_by_bundle(
            om_leaf, app_db_ca
        ), "the AppDB customer CA must not verify the Ops Manager certificate"
        _assert_signed_by(app_db_leaf, app_db_ca, "the legacy AppDB certificate")
        assert not cert_signed_by_bundle(
            app_db_leaf, om_ca
        ), "the Ops Manager customer CA must not verify the AppDB certificate"

        legacy_pem = read_secret(namespace, f"{APP_DB_LEGACY_SECRET}-pem")
        legacy_pem_hash = legacy_pem.get("latestHash", "")
        assert legacy_pem_hash, f"{APP_DB_LEGACY_SECRET}-pem has no latestHash"
        # Assert on a boolean so a failure never prints the PEM material in the dict.
        has_legacy_file = legacy_pem_hash in legacy_pem
        assert has_legacy_file, f"{APP_DB_LEGACY_SECRET}-pem has no data under latestHash; keys: {sorted(legacy_pem)}"

        automation_config = ops_manager.get_automation_config_tester().automation_config
        certificate_key_files = [
            (((process.get("args2_6") or {}).get("net") or {}).get("tls") or {}).get("certificateKeyFile")
            for process in automation_config.get("processes") or []
            if process.get("processType") == "mongod"
        ]
        assert len(certificate_key_files) == 3, f"expected 3 mongod processes, got {certificate_key_files}"
        assert all(
            key_file == f"{APP_DB_CERT_DIR}/{legacy_pem_hash}" for key_file in certificate_key_files
        ), f"the legacy automation config must name the legacy PEM file: {certificate_key_files}"

        migration_state.update(
            {
                "om_uid": ops_manager["metadata"]["uid"],
                "names": names,
                "app_db_pem_hash": legacy_pem_hash,
                "app_db_pem_digest": _value_digest(legacy_pem[legacy_pem_hash]),
                "app_db_key_files": certificate_key_files,
                "app_db_ca_file": (automation_config.get("tls") or {}).get("CAFilePath"),
                "legacy_secret_state": _secret_state(namespace, OM_LEGACY_SECRET),
                "legacy_app_db_secret_state": _secret_state(namespace, APP_DB_LEGACY_SECRET),
                "legacy_om_ca_state": _config_map_state(namespace, OM_LEGACY_CA_CONFIG_MAP),
                "legacy_app_db_ca_state": _config_map_state(namespace, APP_DB_LEGACY_CA_CONFIG_MAP),
            }
        )

    def test_migrate_to_managed_certificates(self, ops_manager: MongoDBOpsManager, migration_state: dict):
        assert migration_state, "the legacy baseline must be captured before the migration"
        ops_manager.load()

        # One update: both security blocks change together, so TLS is never disabled in between.
        # update() sends a merge patch, so a field is removed by setting it to None.
        security = ops_manager["spec"]["security"]
        security["tls"]["secretRef"] = None
        security["tls"]["ca"] = None
        security["managedCertificate"] = {"issuerRef": {"name": OM_ISSUER, "kind": "Issuer"}}
        app_db_security = ops_manager["spec"]["applicationDatabase"]["security"]
        app_db_security["certsSecretPrefix"] = None
        app_db_security["tls"]["ca"] = None
        app_db_security["managedCertificate"] = {"server": {"issuerRef": {"name": APP_DB_ISSUER, "kind": "Issuer"}}}
        ops_manager.update()

        # Wait for the current generation, not a Running phase left over from the legacy deployment.
        _wait_for_appdb_convergence(ops_manager, 3, timeout=1800)

    def test_managed_certificate_mounts_and_files(
        self,
        ops_manager: MongoDBOpsManager,
        namespace: str,
        migration_state: dict,
    ):
        names: ManagedCertificateNames = migration_state["names"]
        _wait_for_appdb_convergence(ops_manager, 3, timeout=1800)
        wait_for_certificates_ready(ops_manager)

        latest_hash = _wait_for_appdb_pem_secret(namespace, names.app_db_name)
        assert latest_hash != migration_state["app_db_pem_hash"], "the managed AppDB certificate must be a new file"

        managed_pem = read_secret(namespace, names.app_db_pem_secret)
        legacy_hash = migration_state["app_db_pem_hash"]
        # Assert on booleans and digests only, so a failure never prints the PEM material.
        retains_legacy_file = legacy_hash in managed_pem
        assert retains_legacy_file, (
            f"{names.app_db_pem_secret} must keep the legacy AppDB certificate under {legacy_hash}, "
            f"so the pod keeps the file the old automation config names; keys: {sorted(managed_pem)}"
        )
        assert (
            _value_digest(managed_pem[legacy_hash]) == migration_state["app_db_pem_digest"]
        ), "the legacy AppDB certificate in the managed PEM secret must be byte-for-byte unchanged"

        leaf_secret = read_secret(namespace, names.app_db_leaf_certificate)
        holds_issued_leaf = _value_digest(managed_pem[latest_hash]) == _value_digest(
            leaf_secret["tls.crt"] + leaf_secret["tls.key"]
        )
        assert holds_issued_leaf, "the managed AppDB PEM secret must hold the issued leaf under latestHash"

        _wait_for_appdb_automation_config(ops_manager, 3, latest_hash)
        automation_config = ops_manager.get_automation_config_tester().automation_config
        for process in automation_config.get("processes") or []:
            if process.get("processType") != "mongod":
                continue
            key_file = (((process.get("args2_6") or {}).get("net") or {}).get("tls") or {}).get("certificateKeyFile")
            assert (
                key_file == f"{APP_DB_CERT_DIR}/{latest_hash}"
            ), f"the automation config must name the managed certificate file, got {key_file}"

        app_db_ca_bundle = read_configmap(namespace, names.app_db_ca_bundle_config_map)
        assert APP_DB_CA_BUNDLE_KEY in app_db_ca_bundle, (
            f"{names.app_db_ca_bundle_config_map} must keep the fixed {APP_DB_CA_BUNDLE_KEY} key, "
            f"so the old automation config CA path stays valid; keys: {sorted(app_db_ca_bundle)}"
        )
        assert app_db_ca_bundle[APP_DB_CA_BUNDLE_KEY].strip(), f"{names.app_db_ca_bundle_config_map} has an empty CA"
        assert (
            app_db_ca_bundle["latestHash"] in app_db_ca_bundle
        ), f"{names.app_db_ca_bundle_config_map} must publish its bundle under latestHash"
        assert (automation_config.get("tls") or {}).get("CAFilePath") != migration_state[
            "app_db_ca_file"
        ], "the managed AppDB automation config must move off the legacy fixed ca-pem path"

        app_db_statefulset = ops_manager.read_appdb_statefulset()
        volumes = app_db_statefulset.spec.template.spec.volumes or []
        pem_volume = next((volume for volume in volumes if volume.name == "secret-certs"), None)
        assert pem_volume is not None and pem_volume.secret is not None
        assert pem_volume.secret.secret_name == names.app_db_pem_secret
        ca_volume = next((volume for volume in volumes if volume.name == "secret-ca"), None)
        assert ca_volume is not None and ca_volume.config_map is not None
        assert ca_volume.config_map.name == names.app_db_ca_bundle_config_map

        om_statefulset = ops_manager.read_statefulset()
        om_volumes = om_statefulset.spec.template.spec.volumes or []
        om_https_volume = next((volume for volume in om_volumes if volume.name == "om-https-certificate"), None)
        assert om_https_volume is not None and om_https_volume.secret is not None
        assert om_https_volume.secret.secret_name == names.om_pem_secret

        om_pem_latest_hash = read_secret(namespace, names.om_pem_secret)["latestHash"]
        assert om_pem_latest_hash, f"{names.om_pem_secret} has no latestHash"
        om_container = next(
            container
            for container in om_statefulset.spec.template.spec.containers
            if container.name == "mongodb-ops-manager"
        )
        pem_env = next((env for env in om_container.env or [] if env.name == OM_PEM_KEY_FILE_ENV_VAR), None)
        assert pem_env is not None, f"the Ops Manager container has no {OM_PEM_KEY_FILE_ENV_VAR}"
        assert pem_env.value.endswith(
            f"/{om_pem_latest_hash}"
        ), f"Ops Manager must serve the managed PEM hash, got {pem_env.value}"

        # The running AppDB pods mount the same managed resources as the template.
        app_db_api_client = get_member_cluster_api_client(ops_manager.pick_one_appdb_member_cluster_name())
        app_db_pods = list_matching_pods(namespace, name_prefix=f"{APP_DB_NAME}-", api_client=app_db_api_client)
        assert len(app_db_pods) == 3, f"expected 3 AppDB pods, got {[pod.metadata.name for pod in app_db_pods]}"
        for pod in app_db_pods:
            mounted_secrets = {
                volume.secret.secret_name for volume in pod.spec.volumes or [] if volume.secret is not None
            }
            mounted_config_maps = {
                volume.config_map.name for volume in pod.spec.volumes or [] if volume.config_map is not None
            }
            assert (
                names.app_db_pem_secret in mounted_secrets
            ), f"{pod.metadata.name} does not mount {names.app_db_pem_secret}: {sorted(mounted_secrets)}"
            assert names.app_db_ca_bundle_config_map in mounted_config_maps, (
                f"{pod.metadata.name} does not mount {names.app_db_ca_bundle_config_map}: "
                f"{sorted(mounted_config_maps)}"
            )

    def test_managed_tls_connectivity_and_monitoring(
        self,
        ops_manager: MongoDBOpsManager,
        namespace: str,
        tmp_path,
        migration_state: dict,
    ):
        names: ManagedCertificateNames = migration_state["names"]
        _wait_for_appdb_convergence(ops_manager, 3, timeout=1800)

        app_db_ca_path = _write_appdb_ca(namespace, tmp_path / "managed-appdb-ca.pem", names.app_db_name)
        _assert_appdb_tls_scram(ops_manager, app_db_ca_path)
        _assert_appdb_connection_rejected(ops_manager.get_appdb_tester())

        ops_manager.assert_appdb_monitoring_group_was_created()
        ops_manager.assert_monitoring_data_exists()

        automation_config_tester = ops_manager.get_automation_config_tester()
        automation_config_tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256", active_auth_mechanism=False)
        assert automation_config_tester.automation_config["tls"]["clientCertificateMode"] == "OPTIONAL"

        om_ca = customer_ca_bundle(namespace, OM_ISSUER)
        app_db_ca = customer_ca_bundle(namespace, APP_DB_ISSUER)
        om_ca_path = tmp_path / "om-customer-ca.pem"
        om_ca_path.write_text(om_ca)
        wait_for_om_https_trusted_by_bundle(ops_manager, om_ca)

        om_leaf = read_secret(namespace, names.om_leaf_certificate)["tls.crt"]
        app_db_leaf = read_secret(namespace, names.app_db_leaf_certificate)["tls.crt"]
        _assert_signed_by(om_leaf, om_ca, "the managed Ops Manager certificate")
        assert not cert_signed_by_bundle(
            om_leaf, app_db_ca
        ), "the AppDB customer CA must not verify the managed Ops Manager certificate"
        _assert_signed_by(app_db_leaf, app_db_ca, "the managed AppDB certificate")
        assert not cert_signed_by_bundle(
            app_db_leaf, om_ca
        ), "the Ops Manager customer CA must not verify the managed AppDB certificate"

        # The endpoint must serve the leaf that was issued, not just any leaf the CA signed.
        assert _served_om_certificate_fingerprint(ops_manager, str(om_ca_path)) == _certificate_fingerprint(
            om_leaf
        ), "Ops Manager must serve the issued managed certificate"

    def test_legacy_resources_unchanged(
        self,
        ops_manager: MongoDBOpsManager,
        namespace: str,
        migration_state: dict,
    ):
        ops_manager.load()
        assert (
            ops_manager["metadata"]["uid"] == migration_state["om_uid"]
        ), "the Ops Manager resource must not be recreated"
        assert ops_manager["spec"]["replicas"] == 1
        assert (ops_manager["spec"]["applicationDatabase"] or {}).get("members") == 3
        assert (ops_manager["spec"]["applicationDatabase"] or {}).get("security", {}).get("certsSecretPrefix", "") in (
            "",
            None,
        ), "the legacy AppDB prefix must be gone after the migration"
        assert ((ops_manager["spec"]["security"] or {}).get("tls") or {}).get("secretRef", {}).get("name") in (
            "",
            None,
        ), "the legacy Ops Manager Secret reference must be gone after the migration"

        assert (
            _secret_state(namespace, OM_LEGACY_SECRET) == migration_state["legacy_secret_state"]
        ), "the user-created Ops Manager certificate Secret must not change"
        assert (
            _secret_state(namespace, APP_DB_LEGACY_SECRET) == migration_state["legacy_app_db_secret_state"]
        ), "the user-created AppDB certificate Secret must not change"
        assert (
            _config_map_state(namespace, OM_LEGACY_CA_CONFIG_MAP) == migration_state["legacy_om_ca_state"]
        ), "the user-created Ops Manager CA ConfigMap must not change"
        assert (
            _config_map_state(namespace, APP_DB_LEGACY_CA_CONFIG_MAP) == migration_state["legacy_app_db_ca_state"]
        ), "the user-created AppDB CA ConfigMap must not change"

        # Legacy Secrets are preserved, but the managed deployment no longer serves their leaves.
        assert _certificate_fingerprint(
            read_secret(namespace, OM_LEGACY_SECRET)["tls.crt"]
        ) != _certificate_fingerprint(read_secret(namespace, migration_state["names"].om_leaf_certificate)["tls.crt"])

        _wait_for_appdb_convergence(ops_manager, 3, timeout=1800)


def _value_digest(value: str) -> str:
    """The digest of one PEM field, so two reads of the same material compare equal."""
    return hashlib.sha256(value.encode()).hexdigest()
