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

"""
Shared helpers for the external-AppDB-via-MongoDB-CR-reference e2e suite
(docs/superpowers/specs/2026-07-02-appdb-mongodb-cr-reference-design.md), split across
om_external_appdb_fresh.py and om_external_appdb_forward.py.
"""

SENTINEL_DOC = {"_id": "external-appdb-sentinel", "marker": "survived-migration"}
TEST_DB = "sentinelDb"
TEST_COLLECTION = "sentinelCollection"
META_OM_NAME = "meta-om"


def password_secret_name(om_name: str) -> str:
    # AppDBSpec.Name() (appdb_types.go:385-387) returns "<om-name>-db", and
    # GetOpsManagerUserPasswordSecretName() delegates to OpsManagerUserPasswordSecretName(m.Name())
    # (appdb_types.go:257,263), so the real secret name is "<om-name>-db-om-password" - the CR's own
    # full name (which must already equal "<om-name>-db" per the naming convention) plus the suffix,
    # not the bare OM name plus the suffix. Verified directly against Go source, not assumed.
    return f"{om_name}-db-om-password"


def ref_kind_for_appdb() -> str:
    return "MongoDBMultiCluster" if is_multi_cluster() else "MongoDB"


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
    """Builds the management-plane Ops Manager ("Meta OM", per the external-AppDB spike topology)
    that owns the projects managing the AppDB-role MongoDB CRs. Deployment happens in each module's
    TestDeployMetaOpsManager class, not here. A single Meta OM is shared by every test class in a
    module - it must never be the Primary OM under test, otherwise the AppDB's automation agents
    would depend on the very OM whose availability depends on that AppDB."""
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("meta_om.yaml"),
        name=META_OM_NAME,
        namespace=namespace,
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    try_load(resource)
    return resource


def configure_appdb_role_mongodb(mdb: MongoDB, meta_om: MongoDBOpsManager, namespace: str) -> MongoDB:
    """Points the AppDB-role CR's project/credentials at the Meta OM (never the Primary OM - see
    ensure_meta_om). Per-CR project name so multiple test classes can share one Meta OM."""
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
