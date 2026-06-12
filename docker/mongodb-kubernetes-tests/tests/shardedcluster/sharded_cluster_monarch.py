"""
e2e test for Monarch Deployment pattern on sharded clusters.

Flow (mirrors replica_set_monarch.py):
  1. Deploy MinIO (S3 store)
  2. Deploy active sharded cluster WITHOUT spec.monarch
  3. Insert documents while cluster is running without DR
  4. Activate Monarch: spec.monarch.role=active → shippers per shard
  5. Verify shippers upload to S3
  6. Deploy standby sharded cluster with spec.monarch.role=standby
  7. Verify data replicated to standby
"""

import time

import pymongo
from kubernetes import client as k8s_client
from pytest import fixture, mark

from kubetester import create_or_update_secret, try_load
from kubetester.create_or_replace_from_yaml import create_or_replace_from_yaml
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from tests.common.monarch_helpers import (
    AGENT_IMAGE,
    INVENTORY_COLLECTION,
    INVENTORY_DOCS,
    MINIO_NAME,
    MINIO_PASSWORD,
    MINIO_USER,
    MONARCH_MIN_MDB_VERSION,
    OM_IMAGE,
    PRODUCTS_DB,
    S3_CREDS_SECRET,
    authed_client,
    create_test_user,
    ensure_s3_bucket,
    minio_endpoint,
    monarch_spec,
    standby_shard_client,
    wait_for_deployment_ready,
    wait_for_monarch_condition,
    wait_for_s3_data,
    wait_for_snapshot_complete_marker,
)

ACTIVE_SC_NAME = "monarch-active-sc"
STANDBY_SC_NAME = "monarch-standby-sc"
SHARD_IDS = ("configRS", "myShard_0", "myShard_1")


def _sanitize_shard_id(shard_id: str) -> str:
    return shard_id.lower().replace("_", "-")


def _shipper_deployment_name(sc_name: str, shard_id: str) -> str:
    return f"{sc_name}-{_sanitize_shard_id(shard_id)}-monarch-shipper"


def _wait_for_all_shippers(namespace: str, sc_name: str, timeout: int = 300):
    for shard_id in SHARD_IDS:
        wait_for_deployment_ready(namespace, _shipper_deployment_name(sc_name, shard_id), timeout=timeout)


@fixture(scope="module")
def ops_manager(namespace: str, custom_mdb_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om-monarch.yaml"), namespace=namespace)
    resource["spec"]["statefulSet"] = {
        "spec": {"template": {"spec": {"containers": [{"name": "mongodb-ops-manager", "image": OM_IMAGE}]}}}
    }
    resource["spec"]["applicationDatabase"]["version"] = custom_appdb_version
    resource.create_admin_secret()
    resource.update()
    resource.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
    resource.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    return resource


@fixture(scope="module")
def minio(namespace: str) -> str:
    apps = k8s_client.AppsV1Api()
    try:
        dep = apps.read_namespaced_deployment(MINIO_NAME, namespace)
        already_ready = dep.status.ready_replicas and dep.status.ready_replicas >= 1
    except k8s_client.exceptions.ApiException as e:
        if e.status != 404:
            raise
        already_ready = False

    if not already_ready:
        api_client = k8s_client.ApiClient()
        create_or_replace_from_yaml(api_client, yaml_fixture("minio.yaml"), namespace=namespace)
        wait_for_deployment_ready(namespace, MINIO_NAME, timeout=120)

    ensure_s3_bucket(namespace)
    return minio_endpoint(namespace)


@fixture(scope="module")
def s3_creds_secret(namespace: str, minio: str) -> str:
    create_or_update_secret(
        namespace=namespace,
        name=S3_CREDS_SECRET,
        data={"awsAccessKeyId": MINIO_USER, "awsSecretAccessKey": MINIO_PASSWORD},
    )
    return S3_CREDS_SECRET


@fixture(scope="module")
def active_sc(namespace: str, ops_manager: MongoDBOpsManager) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("sharded-cluster-monarch.yaml"), ACTIVE_SC_NAME, namespace)
    resource.set_version(MONARCH_MIN_MDB_VERSION)
    resource.configure(ops_manager, ACTIVE_SC_NAME)
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"
    resource["spec"]["podSpec"] = {
        "podTemplate": {"spec": {"containers": [{"name": "mongodb-agent", "image": AGENT_IMAGE}]}}
    }
    try_load(resource)
    return resource


@fixture(scope="module")
def standby_sc(namespace: str, active_sc: MongoDB, s3_creds_secret: str, ops_manager: MongoDBOpsManager) -> MongoDB:
    wait_for_s3_data(namespace, expected_shard_ids=SHARD_IDS)
    resource = MongoDB.from_yaml(yaml_fixture("sharded-cluster-monarch.yaml"), STANDBY_SC_NAME, namespace)
    resource.set_version(MONARCH_MIN_MDB_VERSION)
    resource["spec"]["monarch"] = monarch_spec(
        namespace,
        "standby",
        sourceAgentAuthSecretRef={"name": f"{ACTIVE_SC_NAME}-agent-auth-secret"},
    )
    resource.configure(ops_manager, STANDBY_SC_NAME)
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"
    resource["spec"]["podSpec"] = {
        "podTemplate": {"spec": {"containers": [{"name": "mongodb-agent", "image": AGENT_IMAGE}]}}
    }
    try_load(resource)
    return resource


@fixture(scope="module")
def active_test_user(active_sc: MongoDB, namespace: str) -> MongoDBUser:
    user = create_test_user(active_sc, namespace)
    user.assert_reaches_phase(Phase.Updated, timeout=300)
    return user


@fixture(scope="module")
def standby_test_user(standby_sc: MongoDB, namespace: str) -> MongoDBUser:
    user = create_test_user(standby_sc, namespace)
    user.assert_reaches_phase(Phase.Updated, timeout=300)
    return user


@fixture(scope="module")
def initialize_inventory_documents(active_sc: MongoDB, active_test_user: MongoDBUser) -> int:
    col = authed_client(active_sc)[PRODUCTS_DB][INVENTORY_COLLECTION]
    col.delete_many({})
    col.insert_many(INVENTORY_DOCS)
    count = col.count_documents({})
    assert count == len(INVENTORY_DOCS)
    return count


@mark.e2e_sharded_cluster_monarch
class TestMonarchShipper(KubernetesTester):

    def test_active_sc_running(self, active_sc: MongoDB):
        active_sc.update()
        active_sc.assert_reaches_phase(Phase.Running, timeout=600)

    def test_insert_documents_before_activation(self, initialize_inventory_documents: int):
        assert initialize_inventory_documents == len(INVENTORY_DOCS)

    def test_activate_monarch(self, active_sc: MongoDB, s3_creds_secret: str, namespace: str):
        active_sc["spec"]["monarch"] = monarch_spec(namespace, "active")
        active_sc.update()
        active_sc.assert_reaches_phase(Phase.Running, timeout=600)
        wait_for_monarch_condition(active_sc)
        _wait_for_all_shippers(namespace, ACTIVE_SC_NAME)

    def test_automation_config_has_monarch_components(self, active_sc: MongoDB):
        mc = active_sc.get_automation_config_tester().automation_config["maintainedMonarchComponents"]
        assert len(mc) == 1
        assert mc[0]["replicaSetId"] == f"{ACTIVE_SC_NAME}-config"
        assert mc[0]["initialMode"] == "ACTIVE"
        assert {s["shardId"] for s in mc[0]["shipperConfig"]["shards"]} == set(SHARD_IDS)

    def test_shipper_uploads_to_s3(self, active_sc: MongoDB):
        wait_for_s3_data(self.namespace, expected_shard_ids=SHARD_IDS)

    def test_shipper_emits_snapshot_complete_marker(self, active_sc: MongoDB):
        wait_for_snapshot_complete_marker(self.namespace, expected_shard_ids=SHARD_IDS)


@mark.e2e_sharded_cluster_monarch
class TestMonarchInjector(KubernetesTester):

    def test_standby_sc_running(self, standby_sc: MongoDB, namespace: str):
        standby_sc.update()
        standby_sc.assert_reaches_phase(Phase.Running, timeout=600)
        wait_for_monarch_condition(standby_sc)

    def test_documents_replicated_to_standby(self, standby_sc: MongoDB, standby_test_user: MongoDBUser):
        # The standby's mongos is intentionally disabled by OM/agent standby
        # modifications (the operator only writes the AC; it does not manage the
        # mongos process). configRS members stay on WaitHasCorrectAutomationCredentials
        # because that check probes the disabled mongos, so a mongos-routed read can
        # never succeed pre-promotion. Read each data shard's mongods directly
        # (bypassing mongos) and sum the counts — the inventory collection is sharded,
        # so each shard holds only its chunk range. Mirrors mms-automation e2e.py's
        # standby replication probe. The injector advertises as SECONDARY, so
        # secondaryPreferred reads are required.
        shard_count = standby_sc["spec"]["shardCount"]
        deadline = time.time() + 600
        total = 0
        while time.time() < deadline:
            try:
                total = sum(
                    standby_shard_client(standby_sc, shard_idx)[PRODUCTS_DB][INVENTORY_COLLECTION].count_documents({})
                    for shard_idx in range(shard_count)
                )
                if total == len(INVENTORY_DOCS):
                    break
            except pymongo.errors.PyMongoError:
                pass
            time.sleep(5)
        assert total == len(INVENTORY_DOCS), f"Expected {len(INVENTORY_DOCS)} docs across standby shards, got {total}"
