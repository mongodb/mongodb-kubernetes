import time
from typing import Optional

import pymongo
from kubernetes import client as k8s_client
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.opsmanager import MongoDBOpsManager
from pymongo.errors import AutoReconnect, ServerSelectionTimeoutError
from tests.conftest import get_member_cluster_clients, is_multi_cluster

SENTINEL_DOC = {"_id": "external-appdb-sentinel", "marker": "survived-migration"}
TEST_DB = "sentinelDb"
TEST_COLLECTION = "sentinelCollection"
META_OM_NAME = "meta-om"

# Default cert secret prefix used by create_appdb_certs (tests/common/cert/cert_issuer.py). Both the
# external AppDB MongoDB CR and the internal AppDB resolve their member cert secret to
# "<APPDB_CERT_PREFIX>-<appdb-name>-cert", so an adopted StatefulSet reuses the same certs.
APPDB_CERT_PREFIX = "appdb"


def appdb_tls_security(ca_configmap: str, cert_prefix: str = APPDB_CERT_PREFIX) -> dict:
    """Returns the spec.applicationDatabase.security block enabling TLS for the internal AppDB,
    mirroring fixtures/om_ops_manager_backup_tls.yaml."""
    return {"certsSecretPrefix": cert_prefix, "tls": {"ca": ca_configmap}}


def password_secret_name(om_name: str) -> str:
    return f"{om_name}-db-om-password"


def ref_kind_for_appdb() -> str:
    if is_multicluster():
        return "MongoDBMultiCluster"
    return "MongoDB"


def is_multicluster() -> bool:
    return is_multi_cluster()


def _assert_single_controller_owner_reference(metadata, kind: str, name: str):
    refs = metadata.owner_references or []
    assert len(refs) == 1, (
        f"{metadata.name} must have exactly one ownerReference, got {refs}"
    )
    assert refs[0].kind == kind
    assert refs[0].name == name
    assert refs[0].controller


def _assert_owned_statefulsets(
    name: str, owner_kind: str, owner_name: str, namespace: str
):
    if not is_multicluster():
        sts = k8s_client.AppsV1Api().read_namespaced_stateful_set(name, namespace)
        _assert_single_controller_owner_reference(sts.metadata, owner_kind, owner_name)
        return

    for fallback_index, member_cluster in enumerate(get_member_cluster_clients()):
        cluster_index = (
            member_cluster.cluster_index
            if member_cluster.cluster_index is not None
            else fallback_index
        )
        sts_name = f"{name}-{cluster_index}"
        sts = member_cluster.read_namespaced_stateful_set(sts_name, namespace)
        _assert_single_controller_owner_reference(sts.metadata, owner_kind, owner_name)


def assert_owned_by_mongodb(metadata, name: str):
    """Asserts the resource is owned solely by the external AppDB MongoDB CR."""
    owner_kind = ref_kind_for_appdb()
    _assert_single_controller_owner_reference(metadata, owner_kind, name)
    _assert_owned_statefulsets(name, owner_kind, name, metadata.namespace)


def assert_owned_by_ops_manager(metadata, name: str):
    """Asserts the resource is owned solely by the MongoDBOpsManager resource."""
    _assert_single_controller_owner_reference(metadata, "MongoDBOpsManager", name)


def appdb_role_resource(
    namespace: str, custom_mdb_version: str, name: str
) -> MongoDB | MongoDBMulti:
    """Constructs the MongoDB(role: AppDB) CR that spec.externalApplicationDatabaseRef points at."""
    multicluster = is_multicluster()
    fixture_name = (
        "om_external_appdb_mdbmulti_db.yaml"
        if multicluster
        else "om_external_appdb_db.yaml"
    )
    resource_cls = MongoDBMulti if multicluster else MongoDB
    resource = resource_cls.from_yaml(
        yaml_fixture(fixture_name), name=name, namespace=namespace
    )
    resource.set_version(custom_mdb_version)
    return resource


def meta_om_resource(
    namespace: str, custom_version: Optional[str], custom_appdb_version: str
) -> MongoDBOpsManager:
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


def configure_appdb_role_mongodb(
    mdb: MongoDB | MongoDBMulti, meta_om: MongoDBOpsManager, namespace: str
) -> MongoDB | MongoDBMulti:
    """Points the External AppDB CR's project/credentials at the Meta OM."""
    config_map_name = meta_om.get_or_create_mongodb_connection_config_map(
        mdb.name, f"{mdb.name}-project"
    )

    mdb["spec"]["opsManager"]["configMapRef"]["name"] = config_map_name
    mdb["spec"]["credentials"] = meta_om.api_key_secret(namespace)

    return mdb


def _sentinel_client(
    cnx_string: str, tls_ca_file: Optional[str]
) -> pymongo.MongoClient:
    # a TLS AppDB connection string contains ssl=true, so pymongo needs the CA to verify the server.
    if tls_ca_file is not None:
        return pymongo.MongoClient(
            cnx_string, tlsCAFile=tls_ca_file, serverSelectionTimeoutMS=30000
        )
    return pymongo.MongoClient(cnx_string)


def write_sentinel_doc(cnx_string: str, tls_ca_file: Optional[str] = None):
    client = _sentinel_client(cnx_string, tls_ca_file)
    try:
        client[TEST_DB][TEST_COLLECTION].insert_one(dict(SENTINEL_DOC))
    finally:
        client.close()


def assert_sentinel_doc_present(
    cnx_string: str, tls_ca_file: Optional[str] = None, timeout: int = 0
):
    """Asserts the sentinel document is present.

    With timeout > 0, retries on assertion and connection errors until the timeout — needed after
    a PIT restore, whose job reports FINISHED once OM prepared the files while the agents keep
    applying data afterwards.
    """
    start_time = time.time()
    while True:
        client = _sentinel_client(cnx_string, tls_ca_file)
        try:
            found = client[TEST_DB][TEST_COLLECTION].find_one(
                {"_id": SENTINEL_DOC["_id"]}
            )
            assert found is not None, "sentinel document did not survive the migration"
            assert found["marker"] == SENTINEL_DOC["marker"]
            return
        except (AssertionError, ServerSelectionTimeoutError, AutoReconnect) as e:
            if time.time() - start_time >= timeout:
                if timeout > 0:
                    raise AssertionError(
                        f"sentinel document not found within {timeout}s; last error: {e}"
                    ) from e
                raise
        finally:
            client.close()
        time.sleep(5)


def assert_project_exists(meta_om: MongoDBOpsManager, appdb_name: str):
    """Verifies the AppDB CR's project still exists on the Meta OM after reverse migration."""
    tester = meta_om.get_om_tester(project_name=f"{appdb_name}-project")
    tester.assert_group_exists()


def assert_no_migration_annotations(namespace: str, sts_name: str):
    if not is_multicluster():
        sts = k8s_client.AppsV1Api().read_namespaced_stateful_set(sts_name, namespace)
        annotations = sts.metadata.annotations or {}
        assert "mongodb.com/appdb-migration-ready" not in annotations
        assert "mongodb.com/appdb-reverse-migration-ready" not in annotations
        return

    for fallback_index, member_cluster in enumerate(get_member_cluster_clients()):
        cluster_index = (
            member_cluster.cluster_index
            if member_cluster.cluster_index is not None
            else fallback_index
        )
        sts = member_cluster.read_namespaced_stateful_set(
            f"{sts_name}-{cluster_index}", namespace
        )
        annotations = sts.metadata.annotations or {}
        assert "mongodb.com/appdb-migration-ready" not in annotations
        assert "mongodb.com/appdb-reverse-migration-ready" not in annotations
