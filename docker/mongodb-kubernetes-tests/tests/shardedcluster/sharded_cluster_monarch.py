"""
e2e test for Monarch Deployment pattern on sharded clusters.

Flow:
  1. Deploy MinIO (S3 store)
  2. Deploy active sharded cluster with spec.shardedClusterSpec.monarch.role=active
  3. Verify shipper Deployments created for configRS and each data shard
  4. Verify shippers upload to S3 for all shards
  5. Deploy standby sharded cluster with spec.shardedClusterSpec.monarch.role=standby
  6. Verify injector Deployments created for configRS and each data shard
  7. Verify data replicated to standby
"""

import json
import os
import time

import boto3
import pymongo
import pytest
from botocore.config import Config as BotoConfig
from kubernetes import client as k8s_client
from kubernetes.stream import stream
from pytest import fixture, mark

from kubetester import create_or_update_secret, try_load
from kubetester.create_or_replace_from_yaml import create_or_replace_from_yaml
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase

# ── resource names ──────────────────────────────────────────────────────────
ACTIVE_SC_NAME = "monarch-active-sc"
STANDBY_SC_NAME = "monarch-standby-sc"
MINIO_NAME = "monarch-minio"

# ── S3 / Monarch config ────────────────────────────────────────────────────
S3_BUCKET = "monarch-standby-bucket"
CLUSTER_PREFIX = "failoverdemo"
MINIO_USER = "minioadmin"
MINIO_PASSWORD = "minioadmin123"
AWS_REGION = "eu-north-1"
S3_CREDS_SECRET = "monarch-s3-creds"

# Monarch requires mongod >= 8.0.16 (SERVER-110899 introduced the readBackupFile privilege)
MONARCH_MIN_MDB_VERSION = "8.0.16"

# ── SCRAM auth ──────────────────────────────────────────────────────────────
TEST_USER = "monarch-test-user"
TEST_USER_PASSWORD = "monarch-test-password"
TEST_USER_PASSWORD_SECRET = "monarch-test-user-password"

# ── custom images ───────────────────────────────────────────────────────────
_STAGING_ECR = "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging"
OM_IMAGE = f"{_STAGING_ECR}/mongodb-enterprise-ops-manager-ubi:monarch"
MONARCH_IMAGE = f"{_STAGING_ECR}/mongodb-kubernetes-monarch-injector:monarch"
AGENT_IMAGE = f"{_STAGING_ECR}/mongodb-agent:monarch"

# ── test data ───────────────────────────────────────────────────────────────
PRODUCTS_DB = "products"
INVENTORY_COLLECTION = "inventory"
INVENTORY_DOCS = [
    {"item": "laptop", "qty": 25, "price": 999.99, "warehouse": "A"},
    {"item": "phone", "qty": 100, "price": 699.99, "warehouse": "B"},
    {"item": "tablet", "qty": 50, "price": 449.99, "warehouse": "A"},
]

# ── shard IDs for sharded cluster Monarch ───────────────────────────────────
# configRS + one data shard (shardCount=1)
SHARD_IDS = ("configRS", "myShard_0")


# ── helpers ─────────────────────────────────────────────────────────────────


def _minio_endpoint(namespace: str) -> str:
    return f"http://{MINIO_NAME}.{namespace}.svc.cluster.local:9000"


def _s3_client(endpoint: str):
    return boto3.client(
        "s3",
        endpoint_url=endpoint,
        aws_access_key_id=MINIO_USER,
        aws_secret_access_key=MINIO_PASSWORD,
        region_name=AWS_REGION,
        config=BotoConfig(signature_version="s3v4"),
    )


def _ensure_s3_bucket(namespace: str, timeout: int = 120):
    """Create the S3 bucket, retrying until MinIO is fully ready."""
    s3 = _s3_client(_minio_endpoint(namespace))
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            s3.create_bucket(Bucket=S3_BUCKET)
            return
        except s3.exceptions.BucketAlreadyOwnedByYou:
            return
        except Exception:
            time.sleep(3)
    raise TimeoutError(f"Failed to create S3 bucket {S3_BUCKET} after {timeout}s")


def _wait_for_deployment_ready(namespace: str, name: str, timeout: int = 120):
    """Wait until all desired replicas of a Deployment are Ready."""
    apps = k8s_client.AppsV1Api()
    core = k8s_client.CoreV1Api()
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            dep = apps.read_namespaced_deployment(name, namespace)
            desired = dep.spec.replicas or 0
            ready = dep.status.ready_replicas or 0
            if desired > 0 and ready == desired:
                return
        except k8s_client.exceptions.ApiException:
            pass
        time.sleep(3)
    raise TimeoutError(f"Deployment {name} not fully ready after {timeout}s")


def _wait_for_s3_data(namespace: str, timeout: int = 300, expected_shard_ids: tuple = SHARD_IDS):
    """Wait for at least one S3 object under each expected shard's prefix."""
    s3 = _s3_client(_minio_endpoint(namespace))
    deadline = time.time() + timeout
    pending = set(expected_shard_ids)
    while time.time() < deadline:
        for shard in list(pending):
            if s3.list_objects_v2(Bucket=S3_BUCKET, Prefix=f"{CLUSTER_PREFIX}/{shard}/").get("KeyCount", 0) > 0:
                pending.discard(shard)
        if not pending:
            return
        time.sleep(5)
    raise TimeoutError(f"No S3 objects after {timeout}s for shard(s) {sorted(pending)}")


def _wait_for_snapshot_complete_marker(
    namespace: str, timeout: int = 600, expected_shard_ids: tuple = SHARD_IDS
):
    """Poll S3 for `backups/checkpoint_<ts>_v1/complete` marker per shard."""
    s3 = _s3_client(_minio_endpoint(namespace))
    pending = {shard: f"{CLUSTER_PREFIX}/{shard}/backups/" for shard in expected_shard_ids}
    found: dict = {}
    deadline = time.time() + timeout
    while time.time() < deadline:
        for shard, prefix in list(pending.items()):
            for obj in s3.list_objects_v2(Bucket=S3_BUCKET, Prefix=prefix).get("Contents", []):
                key = obj["Key"]
                if key.endswith("/complete") and "/checkpoint_" in key:
                    found[shard] = key
                    pending.pop(shard)
                    break
        if not pending:
            return found
        time.sleep(10)
    raise TimeoutError(f"No snapshot-complete marker for shard(s) {sorted(pending)} after {timeout}s")


def _authed_mongos_client(sc: MongoDB, *, prefer_secondary: bool = False) -> pymongo.MongoClient:
    """Return a pymongo client connected to mongos, authenticated as TEST_USER."""
    tester = sc.tester()
    kwargs = dict(
        username=TEST_USER,
        password=TEST_USER_PASSWORD,
        authSource="admin",
        authMechanism="SCRAM-SHA-256",
        serverSelectionTimeoutMs=120000,
    )
    if prefer_secondary:
        kwargs["readPreference"] = "secondaryPreferred"
    return pymongo.MongoClient(tester.cnx_string, **kwargs)


def _create_test_user(sc: MongoDB, namespace: str) -> MongoDBUser:
    """Create a SCRAM-SHA-256 user with readWriteAnyDatabase on the sharded cluster."""
    create_or_update_secret(
        namespace=namespace,
        name=TEST_USER_PASSWORD_SECRET,
        data={"password": TEST_USER_PASSWORD},
    )
    user_resource_name = f"{sc.name}-{TEST_USER}"
    user = MongoDBUser(name=user_resource_name, namespace=namespace)
    try_load(user)
    user["spec"] = {
        "username": TEST_USER,
        "db": "admin",
        "passwordSecretKeyRef": {"name": TEST_USER_PASSWORD_SECRET, "key": "password"},
        "mongodbResourceRef": {"name": sc.name},
        "roles": [
            {"db": "admin", "name": "readWriteAnyDatabase"},
            {"db": "admin", "name": "clusterMonitor"},
        ],
    }
    user.update()
    return user


def _monarch_spec_sharded(namespace: str, role: str, **extra) -> dict:
    """Build a Monarch spec for sharded clusters."""
    spec = {
        "role": role,
        "s3": {
            "bucket": S3_BUCKET,
            "region": AWS_REGION,
            "credentialsSecretRef": {"name": S3_CREDS_SECRET},
            "prefix": CLUSTER_PREFIX,
            "endpoint": _minio_endpoint(namespace),
            "pathStyle": True,
        },
        "image": MONARCH_IMAGE,
    }
    spec.update(extra)
    return spec


def _wait_for_monarch_condition_sharded(sc: MongoDB, timeout: int = 300):
    """Wait until ShipperReady or InjectorReady condition is True."""
    role = sc["spec"]["shardedClusterSpec"]["monarch"]["role"]
    condition_type = "ShipperReady" if role == "active" else "InjectorReady"

    def is_ready(resource: MongoDB) -> bool:
        for cond in resource.get("status", {}).get("conditions", []):
            if cond.get("type") == condition_type and cond.get("status") == "True":
                return True
        return False

    sc.wait_for(is_ready, timeout=timeout, should_raise=True)


# ── fixtures ────────────────────────────────────────────────────────────────


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
    """Deploy MinIO if not already running, then ensure the bucket exists."""
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
        _wait_for_deployment_ready(namespace, MINIO_NAME)

    _ensure_s3_bucket(namespace)
    return _minio_endpoint(namespace)


@fixture(scope="module")
def s3_creds_secret(namespace: str, minio: str) -> str:
    create_or_update_secret(
        namespace=namespace,
        name=S3_CREDS_SECRET,
        data={"awsAccessKeyId": MINIO_USER, "awsSecretAccessKey": MINIO_PASSWORD},
    )
    return S3_CREDS_SECRET


@fixture(scope="module")
def active_sc(namespace: str, ops_manager: MongoDBOpsManager, s3_creds_secret: str) -> MongoDB:
    """Active sharded cluster with Monarch shipper."""
    resource = MongoDB.from_yaml(yaml_fixture("sharded-cluster-monarch.yaml"), ACTIVE_SC_NAME, namespace)
    resource.set_version(MONARCH_MIN_MDB_VERSION)
    resource["spec"]["shardedClusterSpec"] = {"monarch": _monarch_spec_sharded(namespace, "active")}
    resource.configure(ops_manager, ACTIVE_SC_NAME)
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"
    resource["spec"]["podSpec"] = {
        "podTemplate": {"spec": {"containers": [{"name": "mongodb-agent", "image": AGENT_IMAGE}]}}
    }
    try_load(resource)
    return resource


@fixture(scope="module")
def standby_sc(
    namespace: str,
    active_sc: MongoDB,
    s3_creds_secret: str,
    ops_manager: MongoDBOpsManager,
) -> MongoDB:
    """Standby sharded cluster with Monarch injector."""
    _wait_for_s3_data(namespace)
    resource = MongoDB.from_yaml(yaml_fixture("sharded-cluster-monarch.yaml"), STANDBY_SC_NAME, namespace)
    resource.set_version(MONARCH_MIN_MDB_VERSION)
    resource["spec"]["shardedClusterSpec"] = {"monarch": _monarch_spec_sharded(namespace, "standby")}
    resource.configure(ops_manager, STANDBY_SC_NAME)
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"
    resource["spec"]["podSpec"] = {
        "podTemplate": {"spec": {"containers": [{"name": "mongodb-agent", "image": AGENT_IMAGE}]}}
    }
    try_load(resource)
    return resource


@fixture(scope="module")
def active_test_user(active_sc: MongoDB, namespace: str) -> MongoDBUser:
    user = _create_test_user(active_sc, namespace)
    user.assert_reaches_phase(Phase.Updated, timeout=300)
    return user


@fixture(scope="module")
def standby_test_user(standby_sc: MongoDB, namespace: str) -> MongoDBUser:
    user = _create_test_user(standby_sc, namespace)
    user.assert_reaches_phase(Phase.Updated, timeout=300)
    return user


@fixture(scope="module")
def initialize_inventory_documents(active_sc: MongoDB, active_test_user: MongoDBUser) -> int:
    col = _authed_mongos_client(active_sc)[PRODUCTS_DB][INVENTORY_COLLECTION]
    col.delete_many({})
    col.insert_many(INVENTORY_DOCS)
    count = col.count_documents({})
    assert count == len(INVENTORY_DOCS)
    return count


# ══════════════════════════════════════════════════════════════════════════════
# OPERATOR INSTALLATION
# ══════════════════════════════════════════════════════════════════════════════


@mark.e2e_sharded_cluster_monarch
def test_install_operator(operator: Operator):
    """Ensure operator is running before any other tests."""
    operator.assert_is_running()


# ══════════════════════════════════════════════════════════════════════════════
# SHIPPER TESTS (Active Sharded Cluster)
# ══════════════════════════════════════════════════════════════════════════════


@mark.e2e_sharded_cluster_monarch
class TestMonarchShipperSharded(KubernetesTester):
    """Tests for active sharded cluster with shipper deployments per shard."""

    def test_active_sc_running(self, active_sc: MongoDB):
        """Deploy active sharded cluster with Monarch spec."""
        active_sc.update()
        active_sc.assert_reaches_phase(Phase.Running, timeout=900)
        _wait_for_monarch_condition_sharded(active_sc)

    def test_shipper_deployments_exist_per_shard(self, active_sc: MongoDB, namespace: str):
        """Verify shipper Deployment exists for configRS and each data shard."""
        apps = k8s_client.AppsV1Api()
        for shard_id in SHARD_IDS:
            dep_name = f"{ACTIVE_SC_NAME}-{shard_id}-monarch-shipper"
            _wait_for_deployment_ready(namespace, dep_name, timeout=300)
            dep = apps.read_namespaced_deployment(dep_name, namespace)
            assert dep.status.ready_replicas == dep.spec.replicas

    def test_shipper_services_exist_per_shard(self, active_sc: MongoDB, namespace: str):
        """Verify Service exists for each shard's shipper."""
        core = k8s_client.CoreV1Api()
        for shard_id in SHARD_IDS:
            svc_name = f"{ACTIVE_SC_NAME}-{shard_id}-monarch-shipper-svc"
            svc = core.read_namespaced_service(svc_name, namespace)
            assert svc is not None

    def test_insert_documents(self, initialize_inventory_documents: int):
        """Insert test documents into the active cluster."""
        assert initialize_inventory_documents == len(INVENTORY_DOCS)

    def test_shippers_upload_to_s3(self, active_sc: MongoDB):
        """Verify shippers upload oplog data to S3 for all shards."""
        _wait_for_s3_data(self.namespace, expected_shard_ids=SHARD_IDS)

    def test_shippers_emit_snapshot_complete_marker(self, active_sc: MongoDB):
        """Verify snapshot-complete marker exists for each shard."""
        _wait_for_snapshot_complete_marker(self.namespace, expected_shard_ids=SHARD_IDS)

    def test_automation_config_has_monarch_components(self, active_sc: MongoDB):
        """Verify AC has maintainedMonarchComponents with all shards."""
        config = active_sc.get_automation_config_tester().automation_config
        mc = config["maintainedMonarchComponents"]
        assert len(mc) == 1
        assert mc[0]["replicaSetId"] == ACTIVE_SC_NAME
        assert mc[0]["initialMode"] == "ACTIVE"

        shipper_shards = mc[0]["shipperConfig"]["shards"]
        shard_ids_in_ac = {s["shardId"] for s in shipper_shards}
        assert shard_ids_in_ac == set(SHARD_IDS), f"Expected {SHARD_IDS}, got {shard_ids_in_ac}"


# ══════════════════════════════════════════════════════════════════════════════
# INJECTOR TESTS (Standby Sharded Cluster)
# ══════════════════════════════════════════════════════════════════════════════


@mark.e2e_sharded_cluster_monarch
class TestMonarchInjectorSharded(KubernetesTester):
    """Tests for standby sharded cluster with injector deployments per shard."""

    def test_standby_sc_running(self, standby_sc: MongoDB, namespace: str):
        """Standby sharded cluster reaches Running with InjectorReady=True."""
        standby_sc.update()
        standby_sc.assert_reaches_phase(Phase.Running, timeout=900)
        _wait_for_monarch_condition_sharded(standby_sc)

    def test_injector_deployments_exist_per_shard(self, standby_sc: MongoDB, namespace: str):
        """Verify injector Deployment exists for configRS and each data shard."""
        apps = k8s_client.AppsV1Api()
        for shard_id in SHARD_IDS:
            dep_name = f"{STANDBY_SC_NAME}-{shard_id}-monarch-injector"
            _wait_for_deployment_ready(namespace, dep_name, timeout=300)
            dep = apps.read_namespaced_deployment(dep_name, namespace)
            assert dep.status.ready_replicas == dep.spec.replicas

    def test_injector_services_exist_per_shard(self, standby_sc: MongoDB, namespace: str):
        """Verify Service exists for each shard's injector."""
        core = k8s_client.CoreV1Api()
        for shard_id in SHARD_IDS:
            svc_name = f"{STANDBY_SC_NAME}-{shard_id}-monarch-injector-svc"
            svc = core.read_namespaced_service(svc_name, namespace)
            assert svc is not None

    def test_standby_automation_config(self, standby_sc: MongoDB):
        """Verify AC has maintainedMonarchComponents with injector config."""
        config = standby_sc.get_automation_config_tester().automation_config
        mc = config["maintainedMonarchComponents"]
        assert mc[0]["replicaSetId"] == STANDBY_SC_NAME
        assert mc[0]["initialMode"] == "STANDBY"

        injector_shards = mc[0]["injectorConfig"]["shards"]
        shard_ids_in_ac = {s["shardId"] for s in injector_shards}
        assert shard_ids_in_ac == set(SHARD_IDS), f"Expected {SHARD_IDS}, got {shard_ids_in_ac}"

    def test_documents_replicated_to_standby(
        self, standby_sc: MongoDB, standby_test_user: MongoDBUser
    ):
        """Verify documents from active cluster are replicated to standby."""
        col = _authed_mongos_client(standby_sc, prefer_secondary=True)[PRODUCTS_DB][INVENTORY_COLLECTION]
        deadline = time.time() + 600
        count = 0
        while time.time() < deadline:
            try:
                count = col.count_documents({})
                if count == len(INVENTORY_DOCS):
                    break
            except pymongo.errors.PyMongoError:
                pass
            time.sleep(5)
        assert count == len(INVENTORY_DOCS), f"Expected {len(INVENTORY_DOCS)} docs on standby, got {count}"
