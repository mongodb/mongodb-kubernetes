import pytest
from kubetester import read_configmap, read_secret
from kubetester.certs import Certificate, Issuer
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
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
from tests.tls.managedcert_helper import managed_certificate_operator

CERT_MANAGER_VERSION = "v1.18.2"
OM_NAME = "om-managedcert"
SELF_SIGNED_ISSUER = f"{OM_NAME}-managed-om-server-selfsigned"
CA_ISSUER = f"{OM_NAME}-managed-om-server-ca-issuer"
CA_CERTIFICATE = f"{OM_NAME}-managed-om-server-ca"
LEAF_CERTIFICATE = f"{OM_NAME}-managed-om-cert"
CA_BUNDLE_CONFIG_MAP = f"{OM_NAME}-managed-om-ca-bundle"
CA_BUNDLE_KEY = "mms-ca.crt"


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
