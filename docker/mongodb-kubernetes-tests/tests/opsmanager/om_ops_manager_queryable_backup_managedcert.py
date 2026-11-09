from collections.abc import Iterator
from datetime import datetime, timedelta, timezone

import pytest
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import NameOID
from kubetester import get_default_storage_class, read_configmap, read_secret
from kubetester.certs import Certificate, Issuer
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pymongo.errors import ServerSelectionTimeoutError
from pytest import fixture
from tests.conftest import (
    get_central_cluster_client,
    get_central_cluster_name,
    install_cert_manager,
    wait_for_cert_manager_ready,
)
from tests.tls.managedcert_helper import managed_certificate_operator

DATABASE = "managed_queryable"
COLLECTION = "documents"
DOCUMENT = {"_id": "managed-backup-certificate", "value": "snapshot data"}


@fixture(scope="module")
def cert_manager() -> str:
    cluster_client = get_central_cluster_client()
    result = install_cert_manager(
        cluster_client=cluster_client,
        cluster_name=get_central_cluster_name(),
        version="v1.18.2",
    )
    wait_for_cert_manager_ready(cluster_client=cluster_client)
    return result


@fixture(scope="module")
def default_operator(cert_manager: str, namespace: str) -> Operator:
    return managed_certificate_operator(namespace)


def _wait_ready(resource):
    def ready() -> bool:
        resource.load()
        return any(
            condition.get("type") == "Ready" and condition.get("status") == "True"
            for condition in resource.backing_obj.get("status", {}).get("conditions", [])
        )

    KubernetesTester.wait_until(ready, timeout=300, sleep_time=2, msg=f"{resource.name} Ready")


@fixture(scope="module")
def issuer(cert_manager: str, namespace: str) -> str:
    root_issuer = Issuer(name="queryable-customer-selfsigned", namespace=namespace)
    root_issuer["spec"] = {"selfSigned": {}}
    root_issuer.update()
    _wait_ready(root_issuer)
    ca = Certificate(name="queryable-customer-ca", namespace=namespace)
    ca["spec"] = {
        "isCA": True,
        "commonName": "queryable-customer-ca",
        "secretName": "queryable-customer-ca",
        "issuerRef": {"name": root_issuer.name, "kind": "Issuer"},
        "usages": ["cert sign", "crl sign"],
    }
    ca.update()
    _wait_ready(ca)
    customer_issuer = Issuer(name="queryable-customer-issuer", namespace=namespace)
    customer_issuer["spec"] = {"ca": {"secretName": ca.name}}
    customer_issuer.update()
    _wait_ready(customer_issuer)
    return customer_issuer.name


@fixture(scope="module")
def certificate_mode() -> str:
    return "selfsigned"


@fixture(scope="module")
def ops_manager(
    namespace: str,
    custom_version: str,
    custom_appdb_version: str,
    certificate_mode: str,
    issuer: str,
) -> Iterator[MongoDBOpsManager]:
    name = f"om-queryable-{certificate_mode}"
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_backup.yaml"), namespace=namespace, name=name)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()
    backup = resource["spec"]["backup"]
    backup.pop("s3Stores", None)
    backup["members"] = 1
    backup["headDB"]["storageClass"] = get_default_storage_class()
    backup["opLogStores"][0]["mongodbResourceRef"]["name"] = f"queryable-oplog-{certificate_mode}"
    backup["blockStores"][0]["mongodbResourceRef"]["name"] = f"queryable-block-{certificate_mode}"
    backup["managedCertificate"] = {"duration": "24h", "renewBefore": "12h"}
    if certificate_mode == "issuer":
        backup["managedCertificate"]["issuerRef"] = {"name": issuer, "kind": "Issuer"}
    resource["spec"]["configuration"]["mongodb.release.autoDownload.enterprise"] = "true"
    resource.update()
    yield resource
    resource.delete()


@fixture(scope="module")
def backing_databases(
    ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str, certificate_mode: str
) -> Iterator[list[MongoDB]]:
    resources = []
    for role in ("oplog", "block"):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"), namespace=namespace, name=f"queryable-{role}-{certificate_mode}"
        ).configure(ops_manager, f"queryable-{role}-{certificate_mode}")
        resource.set_version(ensure_ent_version(custom_mdb_version))
        resource.update()
        resources.append(resource)
    yield resources
    for resource in resources:
        resource.delete()


@fixture(scope="module")
def backed_up_mongodb(
    ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str, certificate_mode: str
) -> Iterator[MongoDB]:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"), namespace=namespace, name=f"queryable-data-{certificate_mode}"
    ).configure(ops_manager, f"queryable-data-{certificate_mode}")
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.update()
    yield resource
    resource.delete()


def _backup_material(ops_manager: MongoDBOpsManager) -> tuple[str, str, str]:
    prefix = f"{ops_manager.name}-managed-backup"
    issued = read_secret(ops_manager.namespace, f"{prefix}-cert")
    derived = read_secret(ops_manager.namespace, f"{prefix}-pem")["queryable.pem"]
    ca = read_configmap(ops_manager.namespace, f"{prefix}-ca-bundle")["mms-ca.crt"]
    assert issued["tls.crt"] in derived
    assert issued["tls.key"] in derived
    assert ca in derived
    assert cert_signed_by_bundle(issued["tls.crt"], ca)
    return issued["tls.crt"], derived, ca


class ManagedQueryableBackupTests:
    def test_operator(self, default_operator: Operator):
        default_operator.wait_for_operator_ready()

    def test_ops_manager(self, ops_manager: MongoDBOpsManager):
        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1800)
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=1800)

    def test_backing_databases(self, ops_manager: MongoDBOpsManager, backing_databases: list[MongoDB]):
        for resource in backing_databases:
            resource.assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_managed_material(self, ops_manager: MongoDBOpsManager, certificate_mode: str, issuer: str):
        leaf, _, _ = _backup_material(ops_manager)
        certificate = Certificate(
            name=f"{ops_manager.name}-managed-backup-cert", namespace=ops_manager.namespace
        ).load()
        ops_manager.load()
        assert any(
            owner["uid"] == ops_manager["metadata"]["uid"] for owner in certificate["metadata"]["ownerReferences"]
        )
        assert "server auth" in certificate["spec"]["usages"]
        assert "client auth" not in certificate["spec"]["usages"]
        expected_issuer = (
            issuer if certificate_mode == "issuer" else f"{ops_manager.name}-managed-backup-server-ca-issuer"
        )
        assert certificate["spec"]["issuerRef"]["name"] == expected_issuer
        leaf_certificate = x509.load_pem_x509_certificate(leaf.encode())
        assert leaf_certificate.subject, "the queryable signing certificate must have a nonempty issuer identity"
        sans = leaf_certificate.extensions.get_extension_for_class(x509.SubjectAlternativeName).value
        names = sans.get_values_for_type(x509.DNSName)
        domain = ops_manager["spec"].get("clusterDomain") or ops_manager["spec"].get("clusterName") or "cluster.local"
        assert f"{ops_manager.name}-svc.{ops_manager.namespace}.svc.{domain}" in names
        for statefulset in (ops_manager.read_statefulset(), ops_manager.read_backup_statefulset()):
            volume = next(volume for volume in statefulset.spec.template.spec.volumes if volume.name == "queryable-pem")
            assert volume.secret.secret_name == f"{ops_manager.name}-managed-backup-pem"
            container = statefulset.spec.template.spec.containers[0]
            assert any(
                mount.name == "queryable-pem" and mount.mount_path == "/certs/" for mount in container.volume_mounts
            )
            assert any(
                env.name == "OM_PROP_brs_queryable_pem" and env.value == "/certs/queryable.pem" for env in container.env
            )
        assert (
            "security" not in ops_manager["spec"]
        ), "backup certificates must not enable OM HTTPS or AppDB managed TLS"

    def test_query_snapshot(self, ops_manager: MongoDBOpsManager, backed_up_mongodb: MongoDB, certificate_mode: str):
        backed_up_mongodb.assert_reaches_phase(Phase.Running, timeout=900)
        with backed_up_mongodb.tester().client as client:
            client[DATABASE][COLLECTION].replace_one({"_id": DOCUMENT["_id"]}, DOCUMENT, upsert=True)
        backed_up_mongodb.configure_backup(mode="enabled")
        backed_up_mongodb.update()
        tester = ops_manager.get_om_tester(project_name=f"queryable-data-{certificate_mode}")
        tester.wait_until_backup_snapshots_are_ready(expected_count=1)
        _, _, ca = _backup_material(ops_manager)
        records = tester.query_backup(DATABASE, COLLECTION, 600, ca_pem=ca, replica_set_name=backed_up_mongodb.name)
        assert DOCUMENT in records

    def test_downloaded_ca_trust(
        self, ops_manager: MongoDBOpsManager, backed_up_mongodb: MongoDB, certificate_mode: str
    ):
        tester = ops_manager.get_om_tester(project_name=f"queryable-data-{certificate_mode}")
        assert DOCUMENT in tester.query_backup(DATABASE, COLLECTION, 600, replica_set_name=backed_up_mongodb.name)

    def test_wrong_ca_is_rejected(
        self, ops_manager: MongoDBOpsManager, backed_up_mongodb: MongoDB, certificate_mode: str
    ):
        key = ec.generate_private_key(ec.SECP256R1())
        subject = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "unrelated-queryable-ca")])
        now = datetime.now(timezone.utc)
        wrong_ca = (
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
        tester = ops_manager.get_om_tester(project_name=f"queryable-data-{certificate_mode}")
        with pytest.raises(ServerSelectionTimeoutError, match="CERTIFICATE_VERIFY_FAILED|certificate verify failed"):
            tester.query_backup(
                DATABASE,
                COLLECTION,
                600,
                ca_pem=wrong_ca,
                replica_set_name=backed_up_mongodb.name,
                server_selection_timeout_ms=5000,
            )

    def test_reissuance(self, ops_manager: MongoDBOpsManager):
        before_leaf, before_pem, before_ca = _backup_material(ops_manager)
        certificate_name = f"{ops_manager.name}-managed-backup-cert"
        certificate = Certificate(name=certificate_name, namespace=ops_manager.namespace).load()
        revision = certificate["status"]["revision"]
        ops_manager.load()
        ops_manager["spec"]["backup"]["managedCertificate"]["duration"] = "23h"
        ops_manager.update()

        def reissued() -> bool:
            current = Certificate(name=certificate_name, namespace=ops_manager.namespace).load()
            if current.backing_obj.get("status", {}).get("revision", 0) <= revision:
                return False
            return read_secret(ops_manager.namespace, certificate_name)["tls.crt"] != before_leaf

        KubernetesTester.wait_until(reissued, timeout=600, sleep_time=5, msg="queryable leaf reissuance")

        def delivered() -> bool:
            source = read_secret(ops_manager.namespace, certificate_name)
            pem = read_secret(ops_manager.namespace, f"{ops_manager.name}-managed-backup-pem")["queryable.pem"]
            return source["tls.crt"] in pem and source["tls.key"] in pem and pem != before_pem

        KubernetesTester.wait_until(delivered, timeout=300, sleep_time=2, msg="renewed queryable PEM")
        _, _, renewed_ca = _backup_material(ops_manager)
        assert renewed_ca == before_ca


@pytest.mark.e2e_om_ops_manager_queryable_backup_managedcert
class TestSelfSignedManagedQueryableBackup(ManagedQueryableBackupTests):
    pass
