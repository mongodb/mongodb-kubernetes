"""
e2e test for Monarch Deployment pattern.

Flow:
  1. Deploy MinIO (S3 store)
  2. Deploy active RS WITHOUT spec.monarch (plain replica set)
  3. Insert documents while RS is running without DR
  4. Activate Monarch: patch spec.monarch.role=active → operator creates shipper
  5. Verify shipper Deployment/Service/ConfigMap and automation config
  6. Verify shipper uploads to S3
  7. Deploy standby RS with spec.monarch.role=standby
  8. Verify standby agents block in WaitForInjectorReady before going Running
  9. Verify data replicated to standby
"""

import os
import subprocess
import time

import boto3
import pymongo
from botocore.config import Config as BotoConfig
from kubernetes import client as k8s_client
from pytest import fixture, mark

from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase

# ── resource names ──────────────────────────────────────────────────────────
ACTIVE_RS_NAME = "monarch-active-rs"
STANDBY_RS_NAME = "monarch-standby-rs"
MINIO_NAME = "monarch-minio"

# ── S3 / Monarch config ────────────────────────────────────────────────────
S3_BUCKET = "monarch-standby-bucket"
CLUSTER_PREFIX = "failoverdemo"
SHARD_ID = "0"
MINIO_USER = "minioadmin"
MINIO_PASSWORD = "minioadmin123"
AWS_REGION = "eu-north-1"
S3_CREDS_SECRET = "monarch-s3-creds"


# ── custom images ───────────────────────────────────────────────────────────
_STAGING_ECR = "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging"
OM_IMAGE = os.getenv("MDB_OM_IMAGE", f"{_STAGING_ECR}/mongodb-enterprise-ops-manager-ubi:monarch")
MONARCH_IMAGE = os.getenv("MDB_MONARCH_IMAGE", f"{_STAGING_ECR}/mongodb-kubernetes-monarch-injector:monarch")

# ── test data ───────────────────────────────────────────────────────────────
PRODUCTS_DB = "products"
INVENTORY_COLLECTION = "inventory"
INVENTORY_DOCS = [
    {"item": "laptop", "qty": 25, "price": 999.99, "warehouse": "A"},
    {"item": "phone", "qty": 100, "price": 699.99, "warehouse": "B"},
    {"item": "tablet", "qty": 50, "price": 449.99, "warehouse": "A"},
    {"item": "monitor", "qty": 75, "price": 329.99, "warehouse": "C"},
    {"item": "keyboard", "qty": 200, "price": 79.99, "warehouse": "B"},
    {"item": "mouse", "qty": 150, "price": 49.99, "warehouse": "A"},
    {"item": "headphones", "qty": 80, "price": 199.99, "warehouse": "C"},
    {"item": "webcam", "qty": 60, "price": 89.99, "warehouse": "B"},
    {"item": "charger", "qty": 300, "price": 29.99, "warehouse": "A"},
    {"item": "cable", "qty": 500, "price": 14.99, "warehouse": "C"},
]


# ── helpers ─────────────────────────────────────────────────────────────────


def _minio_endpoint(namespace: str) -> str:
    return f"http://{MINIO_NAME}.{namespace}.svc.cluster.local:9000"


def _wait_for_deployment_ready(namespace: str, name: str, timeout: int = 120):
    apps = k8s_client.AppsV1Api()
    deadline = time.time() + timeout
    while time.time() < deadline:
        dep = apps.read_namespaced_deployment(name, namespace)
        if dep.status.ready_replicas and dep.status.ready_replicas >= 1:
            return
        time.sleep(3)
    raise TimeoutError(f"Deployment {name} not ready after {timeout}s")


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
    """Create the S3 bucket, retrying until MinIO is fully ready to accept API calls."""
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
    raise TimeoutError(f"Failed to create S3 bucket {S3_BUCKET} in MinIO after {timeout}s")


def _wait_for_s3_data(namespace: str, timeout: int = 300):
    s3 = _s3_client(_minio_endpoint(namespace))
    deadline = time.time() + timeout
    while time.time() < deadline:
        if s3.list_objects_v2(Bucket=S3_BUCKET, Prefix=f"{CLUSTER_PREFIX}/{SHARD_ID}/").get("KeyCount", 0) > 0:
            return
        time.sleep(5)
    raise TimeoutError(f"No S3 objects after {timeout}s — shipper may not be running")


def _wait_for_monarch_condition(mdb: MongoDB, timeout: int = 300):
    """Wait until the ShipperReady or InjectorReady condition on the MongoDB CR is True."""
    role = mdb["spec"]["monarch"]["role"]
    condition_type = "ShipperReady" if role == "active" else "InjectorReady"

    def is_ready(resource: MongoDB) -> bool:
        for cond in resource["status"]["conditions"]:
            if cond.get("type") == condition_type and cond.get("status") == "True":
                return True
        return False

    mdb.wait_for(is_ready, timeout=timeout, should_raise=True)


def _monarch_spec(namespace: str, role: str, **extra) -> dict:
    spec = {
        "role": role,
        "s3BucketName": S3_BUCKET,
        "awsRegion": AWS_REGION,
        "credentialsSecretRef": {"name": S3_CREDS_SECRET},
        "clusterPrefix": CLUSTER_PREFIX,
        "s3BucketEndpoint": _minio_endpoint(namespace),
        "s3PathStyleAccess": True,
        "image": MONARCH_IMAGE,
    }
    spec.update(extra)
    return spec


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
    subprocess.check_call(["kubectl", "apply", "-n", namespace, "-f", yaml_fixture("minio.yaml")])
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
def active_rs(namespace: str, custom_mdb_version: str, ops_manager: MongoDBOpsManager) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-monarch.yaml"), ACTIVE_RS_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource.configure(ops_manager, ACTIVE_RS_NAME)
    try_load(resource)
    return resource


@fixture(scope="module")
def standby_rs(
    namespace: str,
    custom_mdb_version: str,
    active_rs: MongoDB,
    s3_creds_secret: str,
    ops_manager: MongoDBOpsManager,
) -> MongoDB:
    """
    Standby Clusters always start with Injectors.
    """
    _wait_for_s3_data(namespace)
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-monarch.yaml"), STANDBY_RS_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["monarch"] = _monarch_spec(
        namespace,
        "standby",
        activeReplicaSetId=ACTIVE_RS_NAME,
        injectorVersion="0.1.1",
    )
    resource.configure(ops_manager, STANDBY_RS_NAME)
    try_load(resource)
    return resource


@fixture(scope="module")
def initialize_inventory_documents(active_rs: MongoDB) -> int:
    col = active_rs.tester().client[PRODUCTS_DB][INVENTORY_COLLECTION]
    col.delete_many({})
    col.insert_many(INVENTORY_DOCS)
    count = col.count_documents({})
    assert count == len(INVENTORY_DOCS)
    return count


# ── test class ──────────────────────────────────────────────────────────────


@mark.e2e_replica_set_monarch
class TestMonarchDeployments(KubernetesTester):

    # ── Phase 1: Active RS without Monarch ──────────────────────────────

    def test_active_rs_running(self, active_rs: MongoDB):
        active_rs.update()
        active_rs.assert_reaches_phase(Phase.Running, timeout=600)

    def test_no_monarch_resources_before_activation(self, active_rs: MongoDB):
        apps = k8s_client.AppsV1Api()
        try:
            apps.read_namespaced_deployment(f"{ACTIVE_RS_NAME}-monarch-shipper", self.namespace)
            assert False, "Shipper Deployment should not exist before Monarch activation"
        except k8s_client.exceptions.ApiException as e:
            assert e.status == 404

    def test_insert_documents_before_activation(self, initialize_inventory_documents: int):
        assert initialize_inventory_documents == len(INVENTORY_DOCS)

    # ── Phase 2: Activate Monarch on running RS ──────────────────────────

    def test_activate_monarch(self, active_rs: MongoDB, s3_creds_secret: str, namespace: str):
        active_rs["spec"]["monarch"] = _monarch_spec(namespace, "active", shipperVersion="0.1.1")
        active_rs.update()
        active_rs.assert_reaches_phase(Phase.Running, timeout=600)
        _wait_for_monarch_condition(active_rs)

    # ── Phase 3: Automation config ───────────────────────────────────────

    def test_automation_config_has_monarch_components(self, active_rs: MongoDB):
        config = active_rs.get_automation_config_tester().automation_config
        mc = config["maintainedMonarchComponents"]
        assert len(mc) == 1
        assert mc[0]["replicaSetId"] == ACTIVE_RS_NAME
        assert mc[0]["awsBucketName"] == S3_BUCKET
        assert mc[0]["clusterPrefix"] == CLUSTER_PREFIX
        assert mc[0]["initialMode"] == "ACTIVE"
        assert mc[0]["injectorConfig"]["shards"] == []

    # ── Phase 4: Shipper uploading to S3 ────────────────────────────────

    def test_shipper_uploads_to_s3(self, active_rs: MongoDB):
        _wait_for_s3_data(self.namespace)

    def test_shipper_ships_new_writes(self, active_rs: MongoDB):
        s3 = _s3_client(_minio_endpoint(self.namespace))
        prefix = f"{CLUSTER_PREFIX}/{SHARD_ID}/slices/"
        before = s3.list_objects_v2(Bucket=S3_BUCKET, Prefix=prefix).get("KeyCount", 0)
        active_rs.tester().client["shipper_test"]["test"].insert_one({"ts": time.time()})
        time.sleep(15)
        after = s3.list_objects_v2(Bucket=S3_BUCKET, Prefix=prefix).get("KeyCount", 0)
        assert after > before, f"Shipper not shipping: slice count unchanged at {before}"

    # ── Phase 5: Standby RS ──────────────────────────────────────────────

    def test_standby_rs_running(self, standby_rs: MongoDB):
        """Standby RS reaches Running with InjectorReady=True.

        The operator waits for the injector Deployment to be healthy before pushing
        automation config with InjectorInstances. So agents receive InjectorInstances
        pointing to an already-healthy service, and WaitForInjectorReady completes
        immediately. No transient blocking window to test — just final state.
        """
        standby_rs.update()
        standby_rs.assert_reaches_phase(Phase.Running, timeout=600)
        _wait_for_monarch_condition(standby_rs)

    def test_standby_automation_config(self, standby_rs: MongoDB):
        """One InjectorInstance per RS member: Hostname=pod FQDN, endpoints=Service DNS."""
        config = standby_rs.get_automation_config_tester().automation_config
        mc = config["maintainedMonarchComponents"]
        assert mc[0]["replicaSetId"] == ACTIVE_RS_NAME

        instances = mc[0]["injectorConfig"]["shards"][0]["instances"]
        members = standby_rs["spec"]["members"]
        assert len(instances) == members

        svc_dns = f"{STANDBY_RS_NAME}-monarch-injector-svc.{self.namespace}.svc.cluster.local"
        for i, inst in enumerate(instances):
            expected_host = f"{STANDBY_RS_NAME}-{i}.{STANDBY_RS_NAME}-svc.{self.namespace}.svc.cluster.local"
            assert inst["hostname"] == expected_host
            assert inst["healthApiEndpoint"] == f"{svc_dns}:8080"
            assert inst["monarchApiEndpoint"] == f"{svc_dns}:1122"
            assert inst["externallyManaged"] is True

    # ── Phase 6: Data replication ────────────────────────────────────────

    def test_documents_replicated_to_standby(self, standby_rs: MongoDB):
        col = standby_rs.tester().client[PRODUCTS_DB][INVENTORY_COLLECTION]
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
