import json
from pathlib import Path

import kubernetes.client
import pytest
import requests
from cryptography.x509.oid import ExtendedKeyUsageOID
from kubetester import create_or_update_configmap, read_configmap, read_secret
from kubetester.crypto import cert_signed_by_bundle
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.conftest import (
    get_central_cluster_client,
    get_central_cluster_name,
    get_member_cluster_api_client,
    get_member_cluster_clients,
    get_member_cluster_names,
    get_multi_cluster_operator,
    get_multi_cluster_operator_installation_config,
)
from tests.multicluster.conftest import cluster_spec_list
from tests.multicluster_appdb.multicluster_appdb_external_test_helpers import (
    configure_appdb_backup_mongodb_ops_manager,
)
from tests.opsmanager.managedcert_helper import (
    APP_DB_CA_BUNDLE_KEY,
    APP_DB_CA_DIR,
    CA_BUNDLE_KEY,
    ManagedCertificateNames,
    _appdb_cluster_domain,
    _assert_appdb_tls_scram,
    _assert_issued_ekus,
    _assert_owned_by_ops_manager,
    _assert_signed_by,
    _dns_sans,
    _issued_ekus,
    _write_appdb_ca,
    cert_manager,
    latest_ca_bundle_pem,
    wait_for_certificates_ready,
    wait_for_om_https_trusted_by_bundle,
)
from tests.tls.managedcert_helper import member_cert_issuance

OM_NAME = "om-managedcert-mc"
OWNER_LABEL = "mongodb.com/v1.mongodbOpsManagerResourceOwner"
BACKUP_PROJECT = "managedcert-mc-backup"
BACKUP_DATABASE = "managed_queryable"
BACKUP_COLLECTION = "documents"
BACKUP_DOCUMENT = {"_id": "managed-multicluster-backup", "value": "snapshot data"}


@fixture(scope="module")
def multi_cluster_operator(cert_manager: str, namespace: str) -> Operator:
    config = get_multi_cluster_operator_installation_config(namespace)
    config["security.managedCertificate.enabled"] = "true"
    return get_multi_cluster_operator(
        namespace,
        get_central_cluster_name(),
        config,
        get_central_cluster_client(),
        get_member_cluster_clients(),
        get_member_cluster_names(),
        create_managed_certificate_roles=True,
    )


@fixture(scope="module")
def ops_manager(
    multi_cluster_operator: Operator,
    namespace: str,
    custom_version: str,
    custom_appdb_version: str,
    s3_bucket_oplog: str,
    s3_bucket_blockstore: str,
) -> MongoDBOpsManager:
    multi_cluster_operator.wait_for_operator_ready()
    clusters = get_member_cluster_names()[:2]
    assert len(clusters) == 2, "managed OM multi-cluster acceptance requires two member clusters"
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("multicluster_appdb_om.yaml"), OM_NAME, namespace)
    resource.api = kubernetes.client.CustomObjectsApi(get_central_cluster_client())
    resource.set_version(custom_version)
    resource.create_admin_secret(api_client=get_central_cluster_client())
    resource["spec"]["topology"] = "MultiCluster"
    resource["spec"]["clusterSpecList"] = cluster_spec_list(
        clusters, [1, 1], backup_configs=[{"members": 1}, {"members": 1}]
    )
    resource["spec"]["security"] = {"managedCertificate": {}}
    resource["spec"]["backup"] = {
        "enabled": True,
        "managedCertificate": {},
        "headDB": {"storage": "500M"},
    }
    configure_appdb_backup_mongodb_ops_manager(resource, s3_bucket_blockstore, s3_bucket_oplog)
    resource.allow_mdb_rc_versions()
    resource["spec"]["configuration"]["mongodb.release.autoDownload.enterprise"] = "true"
    resource["spec"]["applicationDatabase"] = {
        "topology": "MultiCluster",
        "version": custom_appdb_version,
        "clusterSpecList": cluster_spec_list(clusters, [2, 1]),
        "security": {"managedCertificate": {}},
    }
    resource.update()
    return resource


@fixture(scope="module")
def backed_up_mongodb(ops_manager: MongoDBOpsManager, namespace: str, custom_mdb_version: str) -> MongoDBMulti:
    central = get_central_cluster_client()
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi.yaml"), name="managedcert-mc-data", namespace=namespace
    ).configure(ops_manager, BACKUP_PROJECT, api_client=central)
    resource.api = kubernetes.client.CustomObjectsApi(central)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource["spec"]["persistent"] = True
    resource["spec"]["clusterSpecList"] = cluster_spec_list(get_member_cluster_names()[:2], [2, 1])
    config_name = resource["spec"]["opsManager"]["configMapRef"]["name"]
    config = read_configmap(namespace, config_name, api_client=central)
    config["sslMMSCAConfigMap"] = ManagedCertificateNames.for_ops_manager(ops_manager).om_ca_bundle_config_map
    config["sslRequireValidMMSServerCertificates"] = "true"
    create_or_update_configmap(namespace, config_name, config, api_client=central)
    resource.update()
    return resource


def _wait_for_convergence(ops_manager: MongoDBOpsManager):
    ops_manager.load()
    generation = ops_manager["metadata"]["generation"]

    def converged() -> bool:
        ops_manager.load()
        status = ops_manager.get_status() or {}
        for component in ("applicationDatabase", "opsManager", "backup"):
            component_status = status.get(component) or {}
            if component_status.get("phase") != "Running" or component_status.get("observedGeneration") != generation:
                return False
        for _, spec in ops_manager.get_appdb_indexed_cluster_spec_items():
            sts = ops_manager.read_appdb_statefulset(spec["clusterName"])
            if (
                sts.spec.replicas != spec["members"]
                or sts.status.ready_replicas != spec["members"]
                or sts.status.observed_generation != sts.metadata.generation
            ):
                return False
        for _, spec in ops_manager.get_om_indexed_cluster_spec_items():
            sts = ops_manager.read_backup_statefulset(spec["clusterName"])
            if (
                sts.spec.replicas != 1
                or sts.status.ready_replicas != 1
                or sts.status.observed_generation != sts.metadata.generation
            ):
                return False
        return True

    KubernetesTester.wait_until(converged, timeout=1800, sleep_time=5, msg="managed MC OM/AppDB/backup convergence")
    wait_for_certificates_ready(ops_manager, timeout=1800)


def _assert_appdb_sans(ops_manager: MongoDBOpsManager, certificate: str):
    domain = _appdb_cluster_domain(ops_manager)
    expected = [
        f"{ops_manager.app_db_name()}-{index}-{pod}-svc.{ops_manager.namespace}.svc.{domain}"
        for index, spec in ops_manager.get_appdb_indexed_cluster_spec_items()
        for pod in range(spec["members"])
    ]
    assert sorted(_dns_sans(certificate)) == sorted(expected)
    _assert_issued_ekus(
        certificate,
        {ExtendedKeyUsageOID.SERVER_AUTH, ExtendedKeyUsageOID.CLIENT_AUTH},
        "the AppDB member certificate",
    )


def _assert_ca_bundle_ownership(
    bundle: kubernetes.client.V1ConfigMap, ops_manager: MongoDBOpsManager, cluster_name: str
):
    """Central bundles stay owned by the Ops Manager resource; member copies are label-tracked and unowned.

    The operator runs in the central cluster, which is also a member: it leaves the source
    ConfigMap untouched by UID, while the copies it writes to the other member clusters cannot
    carry owner references across clusters and are cleaned up by label instead.
    """
    owner_references = bundle.metadata.owner_references or []
    if cluster_name == get_central_cluster_name():
        _assert_owned_by_ops_manager(
            bundle,
            ops_manager["metadata"]["uid"],
            f"{bundle.metadata.name} on central cluster {cluster_name}",
        )
        owner = owner_references[0]
        assert owner.kind == "MongoDBOpsManager", (
            f"{bundle.metadata.name} on central cluster {cluster_name} must be owned by the "
            f"MongoDBOpsManager controller, got {owner.kind}"
        )
        assert owner.name == ops_manager.name, (
            f"{bundle.metadata.name} on central cluster {cluster_name} is owned by {owner.name}, "
            f"expected {ops_manager.name}"
        )
        assert owner.controller is True, (
            f"{bundle.metadata.name} on central cluster {cluster_name} must have the Ops Manager "
            f"controller owner reference, got controller={owner.controller}"
        )
        return

    labels = bundle.metadata.labels or {}
    assert labels.get(OWNER_LABEL) == ops_manager.name, (
        f"{bundle.metadata.name} on member cluster {cluster_name} must carry the {OWNER_LABEL} "
        f"label for {ops_manager.name}, got {labels}"
    )
    assert not owner_references, (
        f"{bundle.metadata.name} on member cluster {cluster_name} must not reference the central "
        f"Ops Manager as owner, got {owner_references}"
    )


def _assert_member_delivery(ops_manager: MongoDBOpsManager):
    namespace = ops_manager.namespace
    central = get_central_cluster_client()
    names = ManagedCertificateNames.for_ops_manager(ops_manager)
    appdb_ca = read_configmap(namespace, names.app_db_ca_bundle_config_map, api_client=central)[APP_DB_CA_BUNDLE_KEY]
    om_ca = read_configmap(namespace, names.om_ca_bundle_config_map, api_client=central)[CA_BUNDLE_KEY]
    leaf = read_secret(namespace, names.app_db_leaf_certificate, api_client=central)["tls.crt"]
    _assert_signed_by(leaf, appdb_ca, "the AppDB server certificate")
    assert not cert_signed_by_bundle(leaf, om_ca)

    for _, spec in ops_manager.get_appdb_indexed_cluster_spec_items():
        cluster = get_member_cluster_api_client(spec["clusterName"])
        api = kubernetes.client.CoreV1Api(cluster)
        ca_bundle = api.read_namespaced_config_map(names.app_db_ca_bundle_config_map, namespace)
        assert ca_bundle.data[APP_DB_CA_BUNDLE_KEY] == appdb_ca
        _assert_ca_bundle_ownership(ca_bundle, ops_manager, spec["clusterName"])
        om_bundle = api.read_namespaced_config_map(names.om_ca_bundle_config_map, namespace)
        assert om_bundle.data[CA_BUNDLE_KEY] == om_ca
        _assert_ca_bundle_ownership(om_bundle, ops_manager, spec["clusterName"])
        pem = read_secret(namespace, names.app_db_pem_secret, api_client=cluster)
        current_hash = pem["latestHash"]
        assert leaf in pem[current_hash]
        automation = json.loads(
            read_secret(namespace, f"{names.app_db_name}-config", api_client=cluster)["cluster-config.json"]
        )
        assert automation["tls"]["CAFilePath"] == f"{APP_DB_CA_DIR}/{APP_DB_CA_BUNDLE_KEY}"
        for process in automation["processes"]:
            assert process["args2_6"]["net"]["tls"]["certificateKeyFile"].endswith(f"/certs/{current_hash}")
        sts = ops_manager.read_appdb_statefulset(spec["clusterName"])
        volumes = {volume.name: volume for volume in sts.spec.template.spec.volumes}
        assert volumes["secret-ca"].config_map.name == names.app_db_ca_bundle_config_map
        assert any(
            volume.secret and volume.secret.secret_name == names.app_db_pem_secret for volume in volumes.values()
        )

    for _, spec in ops_manager.get_om_indexed_cluster_spec_items():
        cluster = get_member_cluster_api_client(spec["clusterName"])
        assert (
            read_configmap(namespace, names.app_db_ca_bundle_config_map, api_client=cluster)[APP_DB_CA_BUNDLE_KEY]
            == appdb_ca
        )
        pem = read_secret(namespace, names.om_pem_secret, api_client=cluster)
        assert pem["latestHash"]
        sts = ops_manager.read_statefulset(spec["clusterName"])
        assert any(
            volume.config_map and volume.config_map.name == names.app_db_ca_bundle_config_map
            for volume in sts.spec.template.spec.volumes
        )


def _assert_backup_delivery(ops_manager: MongoDBOpsManager):
    namespace = ops_manager.namespace
    central = get_central_cluster_client()
    prefix = f"{ops_manager.name}-managed-backup"
    issued = read_secret(namespace, f"{prefix}-cert", api_client=central)
    derived = read_secret(namespace, f"{prefix}-pem", api_client=central)["queryable.pem"]
    ca = read_configmap(namespace, f"{prefix}-ca-bundle", api_client=central)[CA_BUNDLE_KEY]
    assert issued["tls.crt"] in derived
    assert issued["tls.key"] in derived
    assert ca in derived
    _assert_signed_by(issued["tls.crt"], ca, "the queryable-backup certificate")
    _assert_issued_ekus(issued["tls.crt"], {ExtendedKeyUsageOID.SERVER_AUTH}, "the queryable-backup certificate")
    assert ExtendedKeyUsageOID.CLIENT_AUTH not in _issued_ekus(issued["tls.crt"])
    certificate = kubernetes.client.CustomObjectsApi(central).get_namespaced_custom_object(
        "cert-manager.io", "v1", namespace, "certificates", f"{prefix}-cert"
    )
    _assert_owned_by_ops_manager(certificate, ops_manager["metadata"]["uid"], "the queryable-backup Certificate")
    domain = ops_manager["spec"].get("clusterDomain") or "cluster.local"
    sans = set(_dns_sans(issued["tls.crt"]))
    assert f"{ops_manager.name}-svc.{namespace}.svc.{domain}" in sans
    for index, spec in ops_manager.get_om_indexed_cluster_spec_items():
        cluster_name = spec["clusterName"]
        cluster = get_member_cluster_api_client(cluster_name)
        daemon_name = f"{ops_manager.name}-{index}-backup-daemon"
        assert f"{daemon_name}-0.{daemon_name}-svc.{namespace}.svc.{domain}" in sans
        assert read_secret(namespace, f"{prefix}-pem", api_client=cluster)["queryable.pem"] == derived
        for sts in (ops_manager.read_statefulset(cluster_name), ops_manager.read_backup_statefulset(cluster_name)):
            volumes = {volume.name: volume for volume in sts.spec.template.spec.volumes}
            assert volumes["queryable-pem"].secret.secret_name == f"{prefix}-pem"
            assert volumes["appdb-ca-certificate"].config_map.name == (
                ManagedCertificateNames.for_ops_manager(ops_manager).app_db_ca_bundle_config_map
            )
            container = sts.spec.template.spec.containers[0]
            assert any(
                mount.name == "queryable-pem" and mount.mount_path == "/certs/" for mount in container.volume_mounts
            )
            env = {item.name: item.value for item in container.env}
            assert env["OM_PROP_brs_queryable_pem"] == "/certs/queryable.pem"


@pytest.mark.e2e_om_managed_certificates_multicluster
class TestMultiClusterManagedOpsManager:
    def test_running_and_monitoring(self, ops_manager: MongoDBOpsManager):
        _wait_for_convergence(ops_manager)
        ops_manager.assert_appdb_monitoring_group_was_created()
        ops_manager.assert_monitoring_data_exists()

    def test_managed_material(self, ops_manager: MongoDBOpsManager):
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        _, leaf = member_cert_issuance(names.app_db_name, ops_manager.namespace)
        _assert_appdb_sans(ops_manager, leaf)
        _assert_member_delivery(ops_manager)
        _assert_backup_delivery(ops_manager)

    def test_verified_tls(self, ops_manager: MongoDBOpsManager, tmp_path: Path):
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        appdb_ca = _write_appdb_ca(ops_manager.namespace, tmp_path / "appdb-ca.pem", names.app_db_name)
        om_ca = latest_ca_bundle_pem(ops_manager.namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY)
        url = ops_manager.om_status().get_url()
        assert url is not None and url.startswith("https://")
        wait_for_om_https_trusted_by_bundle(ops_manager, om_ca)
        with pytest.raises(requests.exceptions.SSLError):
            requests.get(url, verify=appdb_ca, timeout=30)
        _assert_appdb_tls_scram(ops_manager, appdb_ca)

    def test_backup_daemons(self, ops_manager: MongoDBOpsManager):
        tester = ops_manager.get_om_tester(api_client=get_central_cluster_client())
        domain = ops_manager["spec"].get("clusterDomain") or "cluster.local"
        for index, _ in ops_manager.get_om_indexed_cluster_spec_items():
            daemon_name = f"{ops_manager.name}-{index}-backup-daemon"
            tester.assert_daemon_enabled(
                f"{daemon_name}-0.{daemon_name}-svc.{ops_manager.namespace}.svc.{domain}", "/head/"
            )

    def test_queryable_backup(
        self,
        ops_manager: MongoDBOpsManager,
        backed_up_mongodb: MongoDBMulti,
        tmp_path: Path,
        monkeypatch: pytest.MonkeyPatch,
    ):
        backed_up_mongodb.assert_reaches_phase(Phase.Running, timeout=900)
        with backed_up_mongodb.tester().client as client:
            client[BACKUP_DATABASE][BACKUP_COLLECTION].replace_one(
                {"_id": BACKUP_DOCUMENT["_id"]}, BACKUP_DOCUMENT, upsert=True
            )
        backed_up_mongodb.configure_backup(mode="enabled")
        backed_up_mongodb.update()
        backed_up_mongodb.assert_reaches_phase(Phase.Running, timeout=900)
        backed_up_mongodb.assert_backup_reaches_status("STARTED", timeout=900)
        tester = ops_manager.get_om_tester(project_name=BACKUP_PROJECT, api_client=get_central_cluster_client())
        tester.wait_until_backup_snapshots_are_ready(expected_count=1, timeout=1200)
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        om_ca = tmp_path / "om-ca.pem"
        om_ca.write_text(latest_ca_bundle_pem(ops_manager.namespace, names.om_ca_bundle_config_map, CA_BUNDLE_KEY))
        backup_ca = read_configmap(
            ops_manager.namespace,
            f"{ops_manager.name}-managed-backup-ca-bundle",
            api_client=get_central_cluster_client(),
        )[CA_BUNDLE_KEY]
        with monkeypatch.context() as environment:
            environment.setenv("REQUESTS_CA_BUNDLE", str(om_ca))
            records = tester.query_backup(
                BACKUP_DATABASE,
                BACKUP_COLLECTION,
                600,
                ca_pem=backup_ca,
                replica_set_name=backed_up_mongodb.name,
            )
        assert BACKUP_DOCUMENT in records

    def test_scale_and_reorder(self, ops_manager: MongoDBOpsManager, tmp_path: Path):
        names = ManagedCertificateNames.for_ops_manager(ops_manager)
        appdb_ca = _write_appdb_ca(ops_manager.namespace, tmp_path / "appdb-ca.pem", names.app_db_name)
        before_revision, _ = member_cert_issuance(names.app_db_name, ops_manager.namespace)
        indexed_before = {
            spec["clusterName"]: index for index, spec in ops_manager.get_appdb_indexed_cluster_spec_items()
        }
        ops_manager.load()
        specs = ops_manager["spec"]["applicationDatabase"]["clusterSpecList"]
        specs[0]["members"], specs[1]["members"] = 3, 2
        ops_manager.update()
        _wait_for_convergence(ops_manager)
        up_revision, up_leaf = member_cert_issuance(names.app_db_name, ops_manager.namespace)
        assert up_revision == before_revision + 1, "one shared update must cover both clusters' scale targets"
        _assert_appdb_sans(ops_manager, up_leaf)
        _assert_member_delivery(ops_manager)
        _assert_backup_delivery(ops_manager)
        _assert_appdb_tls_scram(ops_manager, appdb_ca)

        ops_manager.load()
        specs = ops_manager["spec"]["applicationDatabase"]["clusterSpecList"]
        specs[0]["members"], specs[1]["members"] = 2, 1
        specs.reverse()
        ops_manager.update()
        _wait_for_convergence(ops_manager)
        down_revision, down_leaf = member_cert_issuance(names.app_db_name, ops_manager.namespace)
        assert down_revision == up_revision
        assert down_leaf == up_leaf, "scale-down must not shrink or reissue the certificate"
        assert {
            spec["clusterName"]: index for index, spec in ops_manager.get_appdb_indexed_cluster_spec_items()
        } == indexed_before
        _assert_member_delivery(ops_manager)
        _assert_backup_delivery(ops_manager)
        _assert_appdb_tls_scram(ops_manager, appdb_ca)
