from pathlib import Path

import pytest
from cryptography import x509
from kubetester import read_configmap, read_secret
from kubetester.certs import Certificate, Issuer
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
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
from tests.tls.managedcert_helper import managed_certificate_operator, member_cert_issuance

CERT_MANAGER_VERSION = "v1.18.2"
OM_NAME = "om-managedcert"
SELF_SIGNED_ISSUER = f"{OM_NAME}-managed-om-server-selfsigned"
CA_ISSUER = f"{OM_NAME}-managed-om-server-ca-issuer"
CA_CERTIFICATE = f"{OM_NAME}-managed-om-server-ca"
LEAF_CERTIFICATE = f"{OM_NAME}-managed-om-cert"
CA_BUNDLE_CONFIG_MAP = f"{OM_NAME}-managed-om-ca-bundle"
CA_BUNDLE_KEY = "mms-ca.crt"

APP_DB_NAME = f"{OM_NAME}-db"
APP_DB_SELF_SIGNED_ISSUER = f"{APP_DB_NAME}-server-selfsigned"
APP_DB_CA_ISSUER = f"{APP_DB_NAME}-server-ca-issuer"
APP_DB_CA_CERTIFICATE = f"{APP_DB_NAME}-server-ca"
APP_DB_LEAF_CERTIFICATE = f"{APP_DB_NAME}-managed-cert"
APP_DB_PEM_SECRET = f"{APP_DB_LEAF_CERTIFICATE}-pem"
APP_DB_CA_BUNDLE_CONFIG_MAP = f"{APP_DB_NAME}-managed-server-ca-bundle"
APP_DB_CA_BUNDLE_KEY = "ca-pem"
APP_DB_CERT_DIR = "/var/lib/mongodb-automation/secrets/certs"
APP_DB_CA_DIR = "/var/lib/mongodb-automation/secrets/ca"
APP_DB_CA_FILE_PATH = f"{APP_DB_CA_DIR}/{APP_DB_CA_BUNDLE_KEY}"
OM_APP_DB_CA_DIR = "/opt/mongodb/mms/ca/"
OM_APP_DB_CA_FILE_PATH = f"{OM_APP_DB_CA_DIR}{APP_DB_CA_BUNDLE_KEY}"
OM_APP_DB_CA_PROPERTY = "mongodb.ssl.CAFile"
OM_APP_DB_CA_ENV_VAR = f"OM_PROP_{OM_APP_DB_CA_PROPERTY.replace('.', '_')}"
APP_DB_CONTAINERS = ("mongodb-agent", "mongod")
OM_CONTAINER = "mongodb-ops-manager"


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


def _assert_owned_by_ops_manager(resource, om_uid: str, description: str):
    owner_references = resource.backing_obj["metadata"].get("ownerReferences", [])
    assert len(owner_references) == 1, f"{description} must have exactly one owner reference, got {owner_references}"
    assert (
        owner_references[0].get("uid") == om_uid
    ), f"{description} is not owned by the Ops Manager UID {om_uid}: {owner_references}"


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


def _dns_sans(cert_pem: str) -> list[str]:
    certificate = x509.load_pem_x509_certificate(cert_pem.encode())
    san = certificate.extensions.get_extension_for_class(x509.SubjectAlternativeName)
    return san.value.get_values_for_type(x509.DNSName)


def _appdb_cluster_domain(ops_manager: MongoDBOpsManager) -> str:
    spec = ops_manager["spec"]
    appdb_spec = spec.get("applicationDatabase") or {}
    return appdb_spec.get("clusterDomain") or spec.get("clusterDomain") or spec.get("clusterName") or "cluster.local"


def _appdb_sans(ops_manager: MongoDBOpsManager, namespace: str, members: int) -> list[str]:
    service_name = f"{APP_DB_NAME}-svc"
    cluster_domain = _appdb_cluster_domain(ops_manager)
    return sorted(
        f"{APP_DB_NAME}-{member}.{service_name}.{namespace}.svc.{cluster_domain}" for member in range(members)
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
    expected_sans: list[str],
    minimum_revision: int | None = None,
    timeout: int = 600,
) -> tuple[int, str]:
    def leaf_is_current() -> bool:
        certificate = Certificate(name=APP_DB_LEAF_CERTIFICATE, namespace=namespace).load()
        revision = certificate["status"].get("revision", 0) if "status" in certificate else 0
        if minimum_revision is not None and revision < minimum_revision:
            return False

        leaf_pem = read_secret(namespace, APP_DB_LEAF_CERTIFICATE).get("tls.crt", "")
        return bool(leaf_pem.strip()) and sorted(_dns_sans(leaf_pem)) == expected_sans

    KubernetesTester.wait_until(
        leaf_is_current,
        timeout=timeout,
        sleep_time=5,
        msg=f"AppDB leaf certificate SANs for {len(expected_sans)} members",
    )
    return member_cert_issuance(APP_DB_NAME, namespace)


def _wait_for_appdb_pem_secret(namespace: str, timeout: int = 600) -> str:
    def pem_is_current() -> bool:
        leaf = read_secret(namespace, APP_DB_LEAF_CERTIFICATE)
        pem = read_secret(namespace, APP_DB_PEM_SECRET)
        latest_hash = pem.get("latestHash", "")
        return bool(latest_hash) and pem.get(latest_hash) == leaf.get("tls.crt", "") + leaf.get("tls.key", "")

    KubernetesTester.wait_until(
        pem_is_current,
        timeout=timeout,
        sleep_time=5,
        msg="AppDB derived PEM secret to match the issued leaf",
    )
    return read_secret(namespace, APP_DB_PEM_SECRET)["latestHash"]


def _wait_for_appdb_automation_config(
    ops_manager: MongoDBOpsManager, expected_members: int, expected_leaf_hash: str, timeout: int = 900
):
    expected_key_file = f"{APP_DB_CERT_DIR}/{expected_leaf_hash}"

    def config_is_current() -> bool:
        automation_config = ops_manager.get_automation_config_tester().automation_config
        if (automation_config.get("tls") or {}).get("CAFilePath") != APP_DB_CA_FILE_PATH:
            return False

        replica_sets = automation_config.get("replicaSets") or []
        if len(replica_sets) != 1 or replica_sets[0].get("_id") != APP_DB_NAME:
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


def _write_appdb_ca(namespace: str, destination: Path) -> str:
    ca_pem = read_configmap(namespace, APP_DB_CA_BUNDLE_CONFIG_MAP).get(APP_DB_CA_BUNDLE_KEY, "")
    assert ca_pem.strip(), f"{APP_DB_CA_BUNDLE_CONFIG_MAP} has no {APP_DB_CA_BUNDLE_KEY}"
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


@fixture(scope="module")
def ops_manager(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_appdb_scram.yaml"), name=OM_NAME, namespace=namespace)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource["spec"]["security"] = {"managedCertificate": {}}
    resource["spec"]["applicationDatabase"]["security"] = {"managedCertificate": {}}
    # Temporary diagnostic: withhold the AppDB client certificate during the TLS startup handshake.
    appdb_mongod_config = resource["spec"]["applicationDatabase"].setdefault("additionalMongodConfig", {})
    appdb_set_parameter = appdb_mongod_config.setdefault("setParameter", {})
    appdb_set_parameter["tlsWithholdClientCertificate"] = True
    resource.update()
    return resource


@pytest.mark.e2e_om_ops_manager_managedcert
class TestOpsManagerManagedCertificates:
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
        _assert_current_certificates_ready(ops_manager)
        om_uid = ops_manager["metadata"]["uid"]

        ca_certificate = Certificate(name=CA_CERTIFICATE, namespace=namespace).load()
        leaf_certificate = Certificate(name=LEAF_CERTIFICATE, namespace=namespace).load()
        self_signed_issuer = Issuer(name=SELF_SIGNED_ISSUER, namespace=namespace).load()
        ca_issuer = Issuer(name=CA_ISSUER, namespace=namespace).load()

        for resource, description in (
            (ca_certificate, CA_CERTIFICATE),
            (leaf_certificate, LEAF_CERTIFICATE),
            (self_signed_issuer, SELF_SIGNED_ISSUER),
            (ca_issuer, CA_ISSUER),
        ):
            _wait_for_ready(resource, description)
            _assert_owned_by_ops_manager(resource, om_uid, description)

        assert ca_certificate["spec"]["secretName"] == CA_CERTIFICATE
        assert ca_certificate["spec"].get("isCA") is True
        assert ca_certificate["spec"]["issuerRef"]["name"] == SELF_SIGNED_ISSUER

        assert "selfSigned" in self_signed_issuer["spec"]
        assert ca_issuer["spec"]["ca"]["secretName"] == CA_CERTIFICATE

        assert leaf_certificate["spec"]["secretName"] == LEAF_CERTIFICATE
        assert leaf_certificate["spec"]["issuerRef"]["name"] == CA_ISSUER

        tls_material = {
            CA_CERTIFICATE: read_secret(namespace, CA_CERTIFICATE),
            LEAF_CERTIFICATE: read_secret(namespace, LEAF_CERTIFICATE),
        }
        for name, secret in tls_material.items():
            assert secret.get("tls.crt"), f"{name} secret has no tls.crt"
            assert secret.get("tls.key"), f"{name} secret has no tls.key"

        ca_bundle = read_configmap(namespace, CA_BUNDLE_CONFIG_MAP).get(CA_BUNDLE_KEY, "")
        assert ca_bundle.strip(), f"{CA_BUNDLE_CONFIG_MAP} has no {CA_BUNDLE_KEY}"
        assert cert_signed_by_bundle(
            tls_material[LEAF_CERTIFICATE]["tls.crt"], ca_bundle
        ), "the managed CA bundle must verify the Ops Manager server certificate"

    def test_appdb_managed_certificates_and_issuers(self, ops_manager: MongoDBOpsManager, namespace: str):
        _wait_for_appdb_convergence(ops_manager, 3)
        _assert_current_certificates_ready(ops_manager)
        om_uid = ops_manager["metadata"]["uid"]

        ca_certificate = Certificate(name=APP_DB_CA_CERTIFICATE, namespace=namespace).load()
        leaf_certificate = Certificate(name=APP_DB_LEAF_CERTIFICATE, namespace=namespace).load()
        self_signed_issuer = Issuer(name=APP_DB_SELF_SIGNED_ISSUER, namespace=namespace).load()
        ca_issuer = Issuer(name=APP_DB_CA_ISSUER, namespace=namespace).load()

        for resource, description in (
            (ca_certificate, APP_DB_CA_CERTIFICATE),
            (leaf_certificate, APP_DB_LEAF_CERTIFICATE),
            (self_signed_issuer, APP_DB_SELF_SIGNED_ISSUER),
            (ca_issuer, APP_DB_CA_ISSUER),
        ):
            _wait_for_ready(resource, description)
            _assert_owned_by_ops_manager(resource, om_uid, description)

        assert ca_certificate["spec"]["secretName"] == APP_DB_CA_CERTIFICATE
        assert ca_certificate["spec"].get("isCA") is True
        assert ca_certificate["spec"]["issuerRef"]["name"] == APP_DB_SELF_SIGNED_ISSUER

        assert "selfSigned" in self_signed_issuer["spec"]
        assert ca_issuer["spec"]["ca"]["secretName"] == APP_DB_CA_CERTIFICATE

        assert leaf_certificate["spec"]["secretName"] == APP_DB_LEAF_CERTIFICATE
        assert leaf_certificate["spec"]["issuerRef"]["name"] == APP_DB_CA_ISSUER
        assert "server auth" in leaf_certificate["spec"].get("usages", [])
        assert "client auth" not in leaf_certificate["spec"].get("usages", [])

        revision, leaf_pem = _wait_for_appdb_leaf(namespace, _appdb_sans(ops_manager, namespace, 3))
        assert revision >= 1

        appdb_ca_secret = read_secret(namespace, APP_DB_CA_CERTIFICATE)
        appdb_leaf_secret = read_secret(namespace, APP_DB_LEAF_CERTIFICATE)
        for name, secret in (
            (APP_DB_CA_CERTIFICATE, appdb_ca_secret),
            (APP_DB_LEAF_CERTIFICATE, appdb_leaf_secret),
        ):
            assert secret.get("tls.crt"), f"{name} secret has no tls.crt"
            assert secret.get("tls.key"), f"{name} secret has no tls.key"
        assert leaf_pem == appdb_leaf_secret["tls.crt"]

        appdb_ca_bundle = read_configmap(namespace, APP_DB_CA_BUNDLE_CONFIG_MAP).get(APP_DB_CA_BUNDLE_KEY, "")
        assert appdb_ca_bundle.strip(), f"{APP_DB_CA_BUNDLE_CONFIG_MAP} has no {APP_DB_CA_BUNDLE_KEY}"
        om_ca_bundle = read_configmap(namespace, CA_BUNDLE_CONFIG_MAP).get(CA_BUNDLE_KEY, "")
        assert om_ca_bundle.strip(), f"{CA_BUNDLE_CONFIG_MAP} has no {CA_BUNDLE_KEY}"
        assert appdb_ca_bundle.strip() != om_ca_bundle.strip(), "Ops Manager and AppDB must keep separate trust chains"
        assert cert_signed_by_bundle(
            leaf_pem, appdb_ca_bundle
        ), "the AppDB CA bundle must verify the AppDB server certificate"
        assert cert_signed_by_bundle(
            leaf_pem, appdb_ca_secret["tls.crt"]
        ), "the AppDB server CA must sign the AppDB server certificate"
        assert not cert_signed_by_bundle(
            leaf_pem, om_ca_bundle
        ), "the independent Ops Manager CA must not verify the AppDB server certificate"

    def test_appdb_managed_tls_reaches_statefulsets_and_automation_config(
        self, ops_manager: MongoDBOpsManager, namespace: str
    ):
        _wait_for_appdb_convergence(ops_manager, 3)
        latest_hash = _wait_for_appdb_pem_secret(namespace)
        _wait_for_appdb_automation_config(ops_manager, 3, latest_hash)

        appdb_statefulset = ops_manager.read_appdb_statefulset()
        appdb_volumes = appdb_statefulset.spec.template.spec.volumes or []
        pem_volume = next((volume for volume in appdb_volumes if volume.name == "secret-certs"), None)
        assert pem_volume is not None, "AppDB StatefulSet has no managed PEM volume"
        pem_secret_volume = pem_volume.secret
        assert pem_secret_volume is not None
        assert pem_secret_volume.secret_name == APP_DB_PEM_SECRET

        ca_volume = next((volume for volume in appdb_volumes if volume.name == "secret-ca"), None)
        assert ca_volume is not None, "AppDB StatefulSet has no managed CA volume"
        ca_config_map_volume = ca_volume.config_map
        assert ca_config_map_volume is not None
        assert ca_config_map_volume.name == APP_DB_CA_BUNDLE_CONFIG_MAP

        for container_name in APP_DB_CONTAINERS:
            _assert_volume_mount(appdb_statefulset, container_name, "secret-certs", APP_DB_CERT_DIR)
            _assert_volume_mount(appdb_statefulset, container_name, "secret-ca", APP_DB_CA_DIR)

        om_statefulset = ops_manager.read_statefulset()
        om_volumes = om_statefulset.spec.template.spec.volumes or []
        om_appdb_ca_volume = next((volume for volume in om_volumes if volume.name == "appdb-ca-certificate"), None)
        assert om_appdb_ca_volume is not None, "Ops Manager StatefulSet has no AppDB CA volume"
        om_appdb_ca_config_map = om_appdb_ca_volume.config_map
        assert om_appdb_ca_config_map is not None
        assert om_appdb_ca_config_map.name == APP_DB_CA_BUNDLE_CONFIG_MAP
        assert om_appdb_ca_config_map.name != CA_BUNDLE_CONFIG_MAP

        _assert_volume_mount(om_statefulset, OM_CONTAINER, "appdb-ca-certificate", OM_APP_DB_CA_DIR)
        _assert_env_value(om_statefulset, OM_CONTAINER, OM_APP_DB_CA_ENV_VAR, OM_APP_DB_CA_FILE_PATH)

        automation_config_tester = ops_manager.get_automation_config_tester()
        automation_config_tester.assert_authentication_mechanism_enabled("SCRAM-SHA-256", active_auth_mechanism=False)
        assert automation_config_tester.automation_config["tls"]["clientCertificateMode"] == "OPTIONAL"

    def test_appdb_verified_tls_scram_and_rejected_connections(
        self, ops_manager: MongoDBOpsManager, namespace: str, tmp_path: Path
    ):
        _wait_for_appdb_convergence(ops_manager, 3)
        _assert_appdb_tls_scram(ops_manager, _write_appdb_ca(namespace, tmp_path / "appdb-ca.pem"))

        _assert_appdb_connection_rejected(ops_manager.get_appdb_tester())

        om_ca_bundle = read_configmap(namespace, CA_BUNDLE_CONFIG_MAP).get(CA_BUNDLE_KEY, "")
        assert om_ca_bundle.strip(), f"{CA_BUNDLE_CONFIG_MAP} has no {CA_BUNDLE_KEY}"
        wrong_ca_path = tmp_path / "ops-manager-ca.pem"
        wrong_ca_path.write_text(om_ca_bundle)
        _assert_appdb_connection_rejected(
            ops_manager.get_appdb_tester(authenticate=True, ssl=True, ca_path=str(wrong_ca_path))
        )

    def test_appdb_scale_up_and_down_with_managed_certificate(
        self, ops_manager: MongoDBOpsManager, namespace: str, tmp_path: Path
    ):
        _wait_for_appdb_convergence(ops_manager, 3)
        _assert_current_certificates_ready(ops_manager)
        revision_before, leaf_before = member_cert_issuance(APP_DB_NAME, namespace)
        assert sorted(_dns_sans(leaf_before)) == _appdb_sans(ops_manager, namespace, 3)

        ops_manager.load()
        ops_manager["spec"]["applicationDatabase"]["members"] = 5
        ops_manager.update()

        _wait_for_appdb_convergence(ops_manager, 5)
        _assert_current_certificates_ready(ops_manager)
        revision_up, leaf_up = _wait_for_appdb_leaf(
            namespace,
            _appdb_sans(ops_manager, namespace, 5),
            minimum_revision=revision_before + 1,
        )
        assert revision_up == revision_before + 1, "the whole scale up must issue the AppDB leaf exactly once"
        assert leaf_up != leaf_before, "the AppDB leaf must cover the new members"
        pem_hash = _wait_for_appdb_pem_secret(namespace)
        _wait_for_appdb_automation_config(ops_manager, 5, pem_hash)
        _assert_appdb_tls_scram(ops_manager, _write_appdb_ca(namespace, tmp_path / "appdb-ca.pem"))

        ops_manager.load()
        ops_manager["spec"]["applicationDatabase"]["members"] = 3
        ops_manager.update()

        _wait_for_appdb_convergence(ops_manager, 3)
        _assert_current_certificates_ready(ops_manager)
        revision_down, leaf_down = member_cert_issuance(APP_DB_NAME, namespace)
        assert revision_down == revision_up, "the AppDB leaf must not be reissued on scale down"
        assert leaf_down == leaf_up, "the AppDB leaf must not change or shrink on scale down"
        assert sorted(_dns_sans(leaf_down)) == _appdb_sans(ops_manager, namespace, 5)
        assert _wait_for_appdb_pem_secret(namespace) == pem_hash
        _wait_for_appdb_automation_config(ops_manager, 3, pem_hash)
        _assert_appdb_tls_scram(ops_manager, _write_appdb_ca(namespace, tmp_path / "appdb-ca.pem"))
