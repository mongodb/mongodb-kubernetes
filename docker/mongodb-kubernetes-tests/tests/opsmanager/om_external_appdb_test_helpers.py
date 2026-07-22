from typing import List, Optional

import kubernetes.client
import pymongo
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.opsmanager import MongoDBOpsManager
from tests.conftest import is_multi_cluster
from tests.multicluster.conftest import cluster_spec_list

SENTINEL_DOC = {"_id": "external-appdb-sentinel", "marker": "survived-migration"}
TEST_DB = "sentinelDb"
TEST_COLLECTION = "sentinelCollection"
META_OM_NAME = "meta-om"


def password_secret_name(om_name: str) -> str:
    return f"{om_name}-db-om-password"


# TODO if only single cluster, simplify
def ref_kind_for_appdb() -> str:
    return "MongoDBMultiCluster" if is_multi_cluster() else "MongoDB"


def _assert_single_controller_owner_reference(metadata, kind: str, name: str):
    refs = metadata.owner_references or []
    assert len(refs) == 1, f"{metadata.name} must have exactly one ownerReference, got {refs}"
    assert refs[0].kind == kind
    assert refs[0].name == name
    assert refs[0].controller


def assert_owned_by_mongodb(metadata, name: str):
    """Asserts the resource is owned solely by the external AppDB CR (MongoDB, or
    MongoDBMultiCluster in multi-cluster runs)."""
    _assert_single_controller_owner_reference(metadata, ref_kind_for_appdb(), name)


def assert_owned_by_ops_manager(metadata, name: str):
    """Asserts the resource is owned solely by the MongoDBOpsManager resource."""
    _assert_single_controller_owner_reference(metadata, "MongoDBOpsManager", name)


# TODO if only single cluster, simplify
def appdb_role_resource(
    namespace: str,
    custom_mdb_version: str,
    name: str,
    member_cluster_names: Optional[List[str]] = None,
    central_cluster_client: Optional[kubernetes.client.ApiClient] = None,
):
    """Kind-aware constructor for the MongoDB(role: AppDB) CR that
    spec.externalApplicationDatabaseRef points at - a plain MongoDB in single-cluster mode, a
    MongoDBMultiCluster (driven by is_multi_cluster()/MEMBER_CLUSTERS) otherwise, mirroring the real
    pattern in tests/multicluster/multi_cluster_scram.py's mongodb_multi fixture."""
    if is_multi_cluster():
        assert member_cluster_names, "member_cluster_names is required in multi-cluster mode"
        resource = MongoDBMulti.from_yaml(
            yaml_fixture("om_external_appdb_db_multi.yaml"), name=name, namespace=namespace
        )
        resource.set_version(custom_mdb_version)

        # appDBRoleRequiresMinimumMembers (mongodb_validation.go) requires >= 3 members total across
        # the cluster spec list for an AppDB-role MongoDBMultiSpec; distribute 1 member per cluster and
        # give the remainder to the first cluster so the total is always >= 3 regardless of how many
        # member clusters are configured.
        counts: List[int | None] = [1] * len(member_cluster_names)
        counts[0] = 1 + max(0, 3 - len(member_cluster_names))
        resource["spec"]["clusterSpecList"] = cluster_spec_list(member_cluster_names, counts)

        if central_cluster_client is not None:
            resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

        return resource
    else:
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


def read_om_pod_restart_counts(ops_manager: MongoDBOpsManager) -> dict[str, int]:
    """Maps Primary OM pod name -> sum of container restartCounts across all its containers."""
    counts = {}
    for api_client, pod in ops_manager.read_om_pods():
        total = sum(cs.restart_count for cs in (pod.status.container_statuses or []))
        counts[pod.metadata.name] = total
    return counts
