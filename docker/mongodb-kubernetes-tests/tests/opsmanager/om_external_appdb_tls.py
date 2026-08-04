from typing import Optional

import kubernetes.client
import pymongo
import pytest
from kubernetes import client as k8s_client
from kubetester import read_secret, try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.common.cert.cert_issuer import create_appdb_certs
from tests.opsmanager.om_external_appdb_test_helpers import (
    APPDB_CERT_PREFIX,
    appdb_role_resource,
    configure_appdb_role_mongodb,
    meta_om_resource,
    ref_kind_for_appdb,
)

"""
E2E test coverage for External AppDB via MongoDB CR reference WITH TLS enabled.

This exercises the TLS/CA parity path: when spec.applicationDatabase is omitted and
spec.externalApplicationDatabaseRef points at a TLS-enabled MongoDB/MongoDBMultiCluster CR, the
operator must resolve the referenced CR's security config and thread its CA ConfigMap into the
Primary OM StatefulSet (mounted at /opt/mongodb/mms/ca/) plus set mongodb.ssl / mongodb.ssl.CAFile
so Ops Manager trusts and connects to the external AppDB over TLS.

If the CA were not mounted (the pre-fix behaviour), the Primary OM would never reach Running because
it could not establish a trusted TLS connection to its AppDB.

The classes form one story on a single Primary OM:
  1. TestDeployMetaOpsManager - deploy the management plane (Meta OM) managing the AppDB CR
  2. TestFreshStartExternalAppDBWithTLS - create the TLS-enabled External AppDB CR, then the Primary
     OM CR referencing it, and assert OM trusts the AppDB's TLS certificate end-to-end.
"""

OM_NAME = "primary-om-ext-appdb-tls"
DB_NAME = f"{OM_NAME}-db"  # must match the operator's required "<om-name>-db" naming convention

# on-disk location the operator mounts the AppDB CA ConfigMap into the OM/BackupDaemon pods
# (util.AppDBMmsCaFileDirInContainer); the mount volume is always named "appdb-ca-certificate".
APPDB_CA_VOLUME_NAME = "appdb-ca-certificate"
APPDB_CA_MOUNT_PATH = "/opt/mongodb/mms/ca/"


@fixture(scope="module")
def meta_om(namespace: str, custom_version: Optional[str], custom_appdb_version: str) -> MongoDBOpsManager:
    return meta_om_resource(namespace, custom_version, custom_appdb_version)


@fixture(scope="module")
def appdb_ca_configmap(app_db_issuer_ca_configmap: str) -> str:
    # ConfigMap "app-db-issuer-ca" (key ca-pem) created from the test issuer's CA; this is the CA the
    # external AppDB CR advertises via security.tls.ca and that the operator must thread into OM.
    return app_db_issuer_ca_configmap


@fixture(scope="module")
def appdb_cert_prefix(namespace: str, issuer: str) -> str:
    return create_appdb_certs(namespace, issuer, DB_NAME, cert_prefix=APPDB_CERT_PREFIX)


@fixture(scope="module")
def external_appdb(
    namespace: str,
    custom_mdb_version: str,
    member_cluster_names,
    central_cluster_client: kubernetes.client.ApiClient,
    appdb_cert_prefix: str,
    appdb_ca_configmap: str,
) -> MongoDB:
    resource = appdb_role_resource(
        namespace,
        custom_mdb_version,
        name=DB_NAME,
        member_cluster_names=member_cluster_names,
        central_cluster_client=central_cluster_client,
    )
    resource.configure_custom_tls(appdb_ca_configmap, appdb_cert_prefix)
    try_load(resource)
    return resource


@fixture(scope="module")
def primary_om(namespace: str, custom_version: Optional[str]) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_primary_om_no_appdb.yaml"),
        name=OM_NAME,
        namespace=namespace,
    )
    resource.set_version(custom_version)
    # the ref stays in code rather than the fixture yaml: its kind is dynamic
    # (MongoDBMultiCluster in multi-cluster runs)
    resource["spec"]["externalApplicationDatabaseRef"] = {"name": DB_NAME, "kind": ref_kind_for_appdb()}
    try_load(resource)
    return resource


@pytest.mark.e2e_om_external_appdb_tls
class TestDeployMetaOpsManager:
    """Deploys the management-plane Ops Manager the AppDB-role MongoDB CR is configured against."""

    def test_deploy_meta_om(self, meta_om: MongoDBOpsManager):
        meta_om.update()
        meta_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        meta_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)


@pytest.mark.e2e_om_external_appdb_tls
class TestFreshStartExternalAppDBWithTLS:
    """Create the TLS-enabled External AppDB (MongoDB role: AppDB) CR managed by Meta OM, then the
    Primary OM CR referencing it with no spec.applicationDatabase."""

    def test_create_tls_appdb(self, external_appdb: MongoDB, meta_om: MongoDBOpsManager, namespace: str):
        configure_appdb_role_mongodb(external_appdb, meta_om, namespace)
        external_appdb.update()
        external_appdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_appdb_tls_is_enabled(self, external_appdb: MongoDB):
        external_appdb.load()
        assert external_appdb["spec"]["security"]["tls"]["enabled"] is True

    def test_create_om_with_ref_and_no_internal_appdb(self, primary_om: MongoDBOpsManager):
        # reaching Running is the core proof: OM only becomes Running once it has established a
        # *trusted* TLS connection to the external AppDB using the CA the operator mounted from the
        # referenced CR. Without the TLS/CA parity fix this would never happen.
        primary_om.update()
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_om_statefulset_mounts_external_appdb_ca(self, primary_om: MongoDBOpsManager, appdb_ca_configmap: str):
        # the OM StatefulSet must mount the referenced CR's CA ConfigMap (not the internal AppDB
        # spec's, which is absent here) at the expected path.
        sts = primary_om.read_statefulset()

        volume = next((v for v in sts.spec.template.spec.volumes if v.name == APPDB_CA_VOLUME_NAME), None)
        assert volume is not None, f"OM StatefulSet is missing the {APPDB_CA_VOLUME_NAME} volume"
        assert volume.config_map.name == appdb_ca_configmap

        om_container = next(c for c in sts.spec.template.spec.containers if c.name == "mongodb-ops-manager")
        mount = next((m for m in om_container.volume_mounts if m.name == APPDB_CA_VOLUME_NAME), None)
        assert mount is not None, "OM container is missing the AppDB CA volume mount"
        assert mount.mount_path == APPDB_CA_MOUNT_PATH

    def test_connection_string_secret_created_with_tls(self, primary_om: MongoDBOpsManager, namespace: str):
        # the OM controller must create the AppDB connection-string secret even for an external AppDB,
        # and its stored connection string must request TLS (ssl=true) since the referenced CR uses TLS.
        secret_name = primary_om.get_appdb_connection_url_secret_name()
        secret_data = read_secret(namespace, secret_name)
        assert "connectionString" in secret_data, f"secret {secret_name} is missing the connectionString key"
        assert "ssl=true" in secret_data["connectionString"]

    def test_connection_string_uses_tls(
        self, primary_om: MongoDBOpsManager, namespace: str, issuer_ca_filepath: str
    ):
        cnx_string = primary_om.read_appdb_connection_url()
        # the referenced CR has TLS enabled, so the operator-computed connection string must request
        # TLS (ssl=true) rather than a plaintext connection.
        assert "ssl=true" in cnx_string

        expected_hosts = {f"{DB_NAME}-{i}.{DB_NAME}-svc.{namespace}.svc.cluster.local:27017" for i in range(3)}
        parsed = pymongo.uri_parser.parse_uri(cnx_string)
        assert {f"{host}:{port}" for host, port in parsed["nodelist"]} == expected_hosts

        # connecting over TLS from the test requires the same CA the operator mounts into OM.
        client = pymongo.MongoClient(cnx_string, tlsCAFile=issuer_ca_filepath, serverSelectionTimeoutMS=30000)
        try:
            hello = client.admin.command("hello")
            assert hello["setName"] == DB_NAME
            assert set(hello["hosts"]) == expected_hosts
        finally:
            client.close()
