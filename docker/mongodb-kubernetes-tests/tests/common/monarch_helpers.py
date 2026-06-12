"""Shared helpers for Monarch e2e tests (replica set and sharded cluster)."""

import time

import boto3
import pymongo
from botocore.config import Config as BotoConfig
from kubernetes import client as k8s_client

from kubetester import create_or_update_secret, try_load
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser

MINIO_NAME = "monarch-minio"

S3_BUCKET = "monarch-standby-bucket"
CLUSTER_PREFIX = "failoverdemo"
DEFAULT_SHARD_IDS = ("0",)

MINIO_USER = "minioadmin"
MINIO_PASSWORD = "minioadmin123"
AWS_REGION = "eu-north-1"
S3_CREDS_SECRET = "monarch-s3-creds"

MONARCH_MIN_MDB_VERSION = "8.0.16"

TEST_USER = "monarch-test-user"
TEST_USER_PASSWORD = "monarch-test-password"
TEST_USER_PASSWORD_SECRET = "monarch-test-user-password"

_STAGING_ECR = "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging"
OM_IMAGE = f"{_STAGING_ECR}/mongodb-enterprise-ops-manager-ubi:monarch"
MONARCH_IMAGE = f"{_STAGING_ECR}/mongodb-kubernetes-monarch-injector:monarch"
AGENT_IMAGE = f"{_STAGING_ECR}/mongodb-agent:monarch"

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


def minio_endpoint(namespace: str) -> str:
    return f"http://{MINIO_NAME}.{namespace}.svc.cluster.local:9000"


def authed_client(mdb: MongoDB, *, prefer_secondary: bool = False) -> pymongo.MongoClient:
    tester = mdb.tester()
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


def standby_shard_client(mdb: MongoDB, shard_idx: int, member_idx: int = 0) -> pymongo.MongoClient:
    """Direct client to a single mongod of a shard, bypassing the standby's disabled mongos.

    A Monarch standby has no primary (the injector advertises as SECONDARY alongside the
    mongod members) and its mongos is intentionally disabled, so the standard mongos-routed
    client can't read it pre-promotion. We connect straight to one shard member with
    directConnection + secondaryPreferred. All members of a shard replicate the same data,
    so member 0 is sufficient; the caller's polling loop tolerates transient errors.
    Mirrors mms-automation e2e.py's standby replication probe (--port + directConnection).
    """
    host = mdb.shard_hostname(shard_idx, member_idx)  # includes :27017
    return pymongo.MongoClient(
        f"mongodb://{host}",
        username=TEST_USER,
        password=TEST_USER_PASSWORD,
        authSource="admin",
        authMechanism="SCRAM-SHA-256",
        directConnection=True,
        readPreference="secondaryPreferred",
        serverSelectionTimeoutMs=120000,
    )


def create_test_user(mdb: MongoDB, namespace: str) -> MongoDBUser:
    create_or_update_secret(
        namespace=namespace,
        name=TEST_USER_PASSWORD_SECRET,
        data={"password": TEST_USER_PASSWORD},
    )
    user = MongoDBUser(name=f"{mdb.name}-{TEST_USER}", namespace=namespace)
    try_load(user)
    user["spec"] = {
        "username": TEST_USER,
        "db": "admin",
        "passwordSecretKeyRef": {"name": TEST_USER_PASSWORD_SECRET, "key": "password"},
        "mongodbResourceRef": {"name": mdb.name},
        "roles": [
            {"db": "admin", "name": "readWriteAnyDatabase"},
            {"db": "admin", "name": "clusterMonitor"},
        ],
    }
    user.update()
    return user


def wait_for_deployment_ready(namespace: str, name: str, timeout: int = 120):
    apps = k8s_client.AppsV1Api()
    core = k8s_client.CoreV1Api()
    deadline = time.time() + timeout
    while time.time() < deadline:
        dep = apps.read_namespaced_deployment(name, namespace)
        desired = dep.spec.replicas or 0
        ready = dep.status.ready_replicas or 0
        if desired > 0 and ready == desired:
            return
        time.sleep(3)

    diag = []
    try:
        selector = dep.spec.selector.match_labels if dep.spec.selector else {}
        label_str = ",".join(f"{k}={v}" for k, v in selector.items())
        pods = core.list_namespaced_pod(namespace, label_selector=label_str)
        for pod in pods.items:
            statuses = pod.status.container_statuses or []
            non_ready = [cs for cs in statuses if not cs.ready]
            if not non_ready:
                continue
            for cs in non_ready:
                diag.append(f"  pod={pod.metadata.name} container={cs.name} restarts={cs.restart_count}")
                for previous in (True, False):
                    try:
                        log = core.read_namespaced_pod_log(
                            pod.metadata.name, namespace,
                            container=cs.name, tail_lines=30, previous=previous,
                        )
                        if log.strip():
                            diag.append(
                                f"  --- {cs.name} {'previous' if previous else 'current'} log (last 30 lines) ---"
                            )
                            for line in log.splitlines():
                                diag.append(f"    {line}")
                            break
                    except Exception:
                        continue
            break
    except Exception as e:
        diag.append(f"  (diagnostic-tail failed: {e})")

    raise TimeoutError(
        f"Deployment {name} not fully ready after {timeout}s (ready={ready}/{desired})\n"
        + "\n".join(diag)
    )


def s3_client(endpoint: str):
    return boto3.client(
        "s3",
        endpoint_url=endpoint,
        aws_access_key_id=MINIO_USER,
        aws_secret_access_key=MINIO_PASSWORD,
        region_name=AWS_REGION,
        config=BotoConfig(signature_version="s3v4"),
    )


def ensure_s3_bucket(namespace: str, timeout: int = 120):
    s3 = s3_client(minio_endpoint(namespace))
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


def wait_for_s3_data(namespace: str, timeout: int = 300, expected_shard_ids: tuple = DEFAULT_SHARD_IDS):
    s3 = s3_client(minio_endpoint(namespace))
    deadline = time.time() + timeout
    pending = set(expected_shard_ids)
    while time.time() < deadline:
        for shard in list(pending):
            if s3.list_objects_v2(Bucket=S3_BUCKET, Prefix=f"{CLUSTER_PREFIX}/{shard}/").get("KeyCount", 0) > 0:
                pending.discard(shard)
        if not pending:
            return
        time.sleep(5)
    raise TimeoutError(
        f"No S3 objects after {timeout}s for shard(s) {sorted(pending)} — shipper may not be running"
    )


def wait_for_snapshot_complete_marker(
    namespace: str, timeout: int = 600, expected_shard_ids: tuple = DEFAULT_SHARD_IDS
):
    s3 = s3_client(minio_endpoint(namespace))
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
    raise TimeoutError(
        f"No `backups/checkpoint_*_v1/complete` marker for shard(s) {sorted(pending)} "
        f"under s3://{S3_BUCKET}/{CLUSTER_PREFIX}/ after {timeout}s — "
        f"shipper may not have completed a snapshot"
    )


def wait_for_monarch_condition(mdb: MongoDB, timeout: int = 300):
    role = mdb["spec"]["monarch"]["role"]
    condition_type = "ShipperReady" if role == "active" else "InjectorReady"

    def is_ready(resource: MongoDB) -> bool:
        for cond in resource["status"].get("conditions", []):
            if cond.get("type") == condition_type and cond.get("status") == "True":
                return True
        return False

    mdb.wait_for(is_ready, timeout=timeout, should_raise=True)


def monarch_spec(namespace: str, role: str, **extra) -> dict:
    spec = {
        "role": role,
        "s3": {
            "bucket": S3_BUCKET,
            "region": AWS_REGION,
            "credentialsSecretRef": {"name": S3_CREDS_SECRET},
            "prefix": CLUSTER_PREFIX,
            "endpoint": minio_endpoint(namespace),
            "pathStyle": True,
        },
        "image": MONARCH_IMAGE,
    }
    spec.update(extra)
    return spec
