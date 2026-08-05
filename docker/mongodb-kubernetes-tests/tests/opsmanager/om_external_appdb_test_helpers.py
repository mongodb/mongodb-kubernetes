from typing import Optional

import pymongo
from kubernetes import client as k8s_client
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager

SENTINEL_DOC = {"_id": "external-appdb-sentinel", "marker": "survived-migration"}
TEST_DB = "sentinelDb"
TEST_COLLECTION = "sentinelCollection"
META_OM_NAME = "meta-om"


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


def write_sentinel_doc(cnx_string: str):
    client = pymongo.MongoClient(cnx_string)
    try:
        client[TEST_DB][TEST_COLLECTION].insert_one(dict(SENTINEL_DOC))
    finally:
        client.close()


def assert_sentinel_doc_present(cnx_string: str):
    client = pymongo.MongoClient(cnx_string)
    try:
        found = client[TEST_DB][TEST_COLLECTION].find_one({"_id": SENTINEL_DOC["_id"]})
        assert found is not None, "sentinel document did not survive the migration"
        assert found["marker"] == SENTINEL_DOC["marker"]
    finally:
        client.close()


def assert_project_exists(meta_om: MongoDBOpsManager, appdb_name: str):
    """Verifies the AppDB CR's project still exists on the Meta OM after reverse migration."""
    tester = meta_om.get_om_tester(project_name=f"{appdb_name}-project")
    tester.assert_group_exists()


def assert_no_migration_annotations(namespace: str, sts_name: str):
    sts = k8s_client.AppsV1Api().read_namespaced_stateful_set(sts_name, namespace)
    annotations = sts.metadata.annotations or {}
    assert "mongodb.com/appdb-migration-ready" not in annotations
    assert "mongodb.com/appdb-reverse-migration-ready" not in annotations
