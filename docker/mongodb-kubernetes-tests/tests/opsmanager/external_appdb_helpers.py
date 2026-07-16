from typing import List, Optional

import kubernetes.client
import pymongo
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
PROJECT_NAME = "external-appdb-project"


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


def configure_appdb_role_mongodb(mdb: MongoDB, ops_manager: MongoDBOpsManager, namespace: str) -> MongoDB:
    config_map_name = ops_manager.get_or_create_mongodb_connection_config_map(mdb.name, PROJECT_NAME)
    mdb["spec"]["opsManager"]["configMapRef"]["name"] = config_map_name
    mdb["spec"]["credentials"] = ops_manager.api_key_secret(namespace)
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
