"""
e2e test for Monarch Deployment pattern including failover (promotion).

Failover model: the operator follows S3, it does NOT implement planned bidirectional
failover via CR edit. To promote, write PromoteStandby directly to the S3 DR state
file (matches the EA playbook). The operator's reconcileMonarchS3State observes the
state change and handleUnplannedPromotion swaps injector → shipper.

Flow:
  1. Deploy MinIO (S3 store)
  2. Deploy active RS WITHOUT spec.monarch (plain replica set)
  3. Insert documents while RS is running without DR
  4. Activate Monarch on active: spec.monarch.role=active → operator creates shipper
  5. Verify shipper uploads to S3
  6. Deploy standby RS with spec.monarch.role=standby → operator creates injector
  7. Verify standby agents reach goal state
  8. Verify data replicated to standby
  9. Trigger failover: write PromoteStandby to S3 DR state file
 10. Verify injector deleted, shipper created, S3 state advanced to Active by agent
 11. Verify promoted cluster can write and shipper uploads to S3
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
from kubetester import create_or_update_secret, try_load
from kubetester.create_or_replace_from_yaml import create_or_replace_from_yaml
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import with_scram
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark

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

# Monarch requires mongod ≥ 8.0.16 (SERVER-110899 introduced the readBackupFile
# privilege that OM's shipperRole depends on). Earlier versions reject createRole
# with `(BadValue) Unknown action type in privilege set: 'readBackupFile'`. Pin
# explicitly so the test isn't silently broken by an older CUSTOM_MDB_VERSION
# default in conftest or in CI.
MONARCH_MIN_MDB_VERSION = "8.0.16"

# ── SCRAM auth (RS fixtures enable it so OM auto-provisions mms-shipper) ────
TEST_USER = "monarch-test-user"
TEST_USER_PASSWORD = "monarch-test-password"
TEST_USER_PASSWORD_SECRET = "monarch-test-user-password"


# ── custom images ───────────────────────────────────────────────────────────
# Hard-coded to the staging ECR images pushed by scripts/dev/build-monarch-images.sh.
# Evergreen has no Monarch-aware build pipeline yet, so the test pins the :monarch
# tag of each image rather than reading from environment variables. Once Evergreen
# has its own Monarch build steps, we can switch back to env-overrideable defaults.
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


def _authed_client(rs: MongoDB, *, prefer_secondary: bool = False) -> pymongo.MongoClient:
    """Return a pymongo client authenticated as TEST_USER (SCRAM-SHA-256).

    The fixture enables SCRAM on the replica set, which in turn lets OM auto-provision
    mms-shipper@admin (needed for FCBIS snapshots). All test ops on application data
    must therefore authenticate; we use a separate test user with readWrite on all
    databases so any test collection works.

    When prefer_secondary is True, the client uses secondaryPreferred read preference.
    Required for Monarch standby clusters: by design they elect no primary — the
    injector replays oplogs into all members as SECONDARY. A `primary`-pinned read
    (pymongo's default) hangs forever waiting for an election that never happens.
    """
    tester = rs.tester()
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


def _create_test_user(rs: MongoDB, namespace: str) -> MongoDBUser:
    """Create a SCRAM-SHA-256 user with readWriteAnyDatabase on the given RS.

    Idempotent — reuses the same password Secret across both RS resources so
    one TEST_USER credential pair works against both active and standby."""
    create_or_update_secret(
        namespace=namespace,
        name=TEST_USER_PASSWORD_SECRET,
        data={"password": TEST_USER_PASSWORD},
    )
    user_resource_name = f"{rs.name}-{TEST_USER}"
    user = MongoDBUser(name=user_resource_name, namespace=namespace)
    try_load(user)
    user["spec"] = {
        "username": TEST_USER,
        "db": "admin",
        "passwordSecretKeyRef": {"name": TEST_USER_PASSWORD_SECRET, "key": "password"},
        "mongodbResourceRef": {"name": rs.name},
        "roles": [
            {"db": "admin", "name": "readWriteAnyDatabase"},
            {"db": "admin", "name": "clusterMonitor"},
        ],
    }
    user.update()
    return user


def _wait_for_deployment_ready(namespace: str, name: str, timeout: int = 120):
    """Wait until all desired replicas of a Deployment are Ready.

    Catches CrashLoopBackOff and similar pod-level failures that the operator
    treats as 'created' but which leave the workload non-functional.

    On timeout, embeds the last 30 log lines from a non-Ready pod's container
    in the TimeoutError message so the failing test's pytest output points at
    the actual cause, no artifact dive needed.
    """
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

    # Timeout — find a non-Ready pod and tail its log so the cause shows up
    # right in the test failure output.
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
                # Try previous container log first (catches CLB where current log is empty),
                # fall back to current.
                for previous in (True, False):
                    try:
                        log = core.read_namespaced_pod_log(
                            pod.metadata.name,
                            namespace,
                            container=cs.name,
                            tail_lines=30,
                            previous=previous,
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
            break  # one non-Ready pod is enough
    except Exception as e:
        diag.append(f"  (diagnostic-tail failed: {e})")

    raise TimeoutError(
        f"Deployment {name} not fully ready after {timeout}s (ready={ready}/{desired})\n" + "\n".join(diag)
    )


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


def _wait_for_s3_data(namespace: str, timeout: int = 300, expected_shard_ids: tuple = (SHARD_ID,)):
    """Wait for at least one S3 object under each expected shard's prefix.

    Parameterized on shard ids to mirror EA's verify_s3 (which iterates
    configRS + every myShard_N) — single-shard today, multi-shard when
    the operator grows sharded-cluster Monarch support.
    """
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
    raise TimeoutError(f"No S3 objects after {timeout}s for shard(s) {sorted(pending)} — shipper may not be running")


def _dr_state_key(cluster_name: str) -> str:
    """S3 key for the DR state file (matches drstate.Client.drStateKey)."""
    return f"{CLUSTER_PREFIX}/dr_status_{cluster_name}.json"


def _write_dr_state(namespace: str, cluster_name: str, state: str, previous_state: str = ""):
    """Write the DR state file to S3. Used to seed bootstrap state and to trigger
    promotion (PromoteStandby) during failover tests."""
    s3 = _s3_client(_minio_endpoint(namespace))
    body = {
        "state": state,
        "previousState": previous_state,
        "clusterName": cluster_name,
        "version": "1",
        "lastModified": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "schemaVersion": "1",
    }
    s3.put_object(
        Bucket=S3_BUCKET,
        Key=_dr_state_key(cluster_name),
        Body=json.dumps(body).encode("utf-8"),
        ContentType="application/json",
    )


def _wait_for_snapshot_complete_marker(namespace: str, timeout: int = 600, expected_shard_ids: tuple = (SHARD_ID,)):
    """Poll S3 for at least one `backups/checkpoint_<ts>_v1/complete` marker
    under each expected shard's prefix.

    Mirrors verify_s3() in mms-automation/docker/e2e-om-infra/e2e.py: a fresh
    `slices/*.bson` listing alone proves the shipper is writing oplog frames,
    but the snapshot-complete marker is what proves the shipper finished a
    full backup checkpoint — the actual gate before standby bootstrap is safe.

    Parameterized on shard ids so the helper is shaped right when sharded
    Monarch lands; single-shard default keeps current call sites unchanged.
    Returns a dict mapping shard_id -> marker key.
    """
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
    raise TimeoutError(
        f"No `backups/checkpoint_*_v1/complete` marker for shard(s) {sorted(pending)} "
        f"under s3://{S3_BUCKET}/{CLUSTER_PREFIX}/ after {timeout}s — "
        f"shipper may not have completed a snapshot"
    )


def _read_dr_state_full(namespace: str, cluster_name: str) -> dict:
    """Read the full DR state JSON; raises if missing or unparseable."""
    s3 = _s3_client(_minio_endpoint(namespace))
    resp = s3.get_object(Bucket=S3_BUCKET, Key=_dr_state_key(cluster_name))
    return json.loads(resp["Body"].read().decode("utf-8"))


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
    """Build a Monarch spec using the simplified API structure."""
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


# ── diagnostics on failure ──────────────────────────────────────────────────
# Captures S3-side state (oplog slices, snapshots, DR state files) and Monarch
# component health endpoints. EVG's existing dump_diagnostic_information.sh
# already captures K8s-side state (pod logs, deployments, CRD conditions),
# but it has no visibility into S3 or the Monarch HTTP APIs. These fixtures
# fill that gap on test failure.

# PHASE_REPORT_KEY + pytest_runtest_makereport live in this directory's
# conftest.py — pytest only auto-discovers hooks from there.
from tests.replicaset.conftest import PHASE_REPORT_KEY


@fixture(autouse=True)
def _dump_monarch_diagnostics_on_failure(request, namespace):
    """Auto-applied to every test in this module. On failure, dumps S3 state and
    Monarch component health to the pytest log dir so EVG attaches them as artifacts."""
    yield
    reports = request.node.stash.get(PHASE_REPORT_KEY, {})
    if not any(r.failed for r in reports.values()):
        return

    test_name = request.node.name.replace("/", "_").replace(":", "_")
    try:
        _dump_s3_state(namespace, test_name)
    except Exception as e:
        print(f"[monarch-diag] S3 dump failed: {e}")
    try:
        _dump_monarch_health(namespace, test_name)
    except Exception as e:
        print(f"[monarch-diag] health dump failed: {e}")
    try:
        _dump_failure_summary(namespace, test_name)
    except Exception as e:
        print(f"[monarch-diag] failure summary failed: {e}")


def _dump_failure_summary(namespace: str, test_name: str):
    """Single consolidated summary file: every signal we'd want before
    descending into the per-pod log haystack. Designed so a future debugger
    can `Read` ONE file and have the answer to "what's wrong" 90% of the
    time. Sections (in order of usefulness): pod readiness, agent plan
    failures, ConfigMap dumps, Secret presence, CR status conditions."""
    out_dir = "/tmp/diagnostics"
    os.makedirs(out_dir, exist_ok=True)
    out_path = f"{out_dir}/monarch-failure-summary-{test_name}.txt"
    core = k8s_client.CoreV1Api()
    custom = k8s_client.CustomObjectsApi()

    with open(out_path, "w") as f:
        # 1. Per-Monarch-pod readiness + last 50 log lines if not Ready
        f.write("=== Monarch pod readiness ===\n")
        pods = core.list_namespaced_pod(namespace, label_selector="monarch-component")
        for pod in pods.items:
            ready = all(cs.ready for cs in (pod.status.container_statuses or []))
            f.write(f"\n--- {pod.metadata.name} ready={ready} phase={pod.status.phase} ---\n")
            for cs in pod.status.container_statuses or []:
                f.write(f"  {cs.name}: ready={cs.ready} restarts={cs.restart_count}\n")
            if not ready:
                # Last 50 lines from the non-Ready container so the smoking
                # gun shows up in this file directly.
                for cs in pod.status.container_statuses or []:
                    if cs.ready:
                        continue
                    f.write(f"\n  ... last 50 lines of {cs.name} ...\n")
                    try:
                        log = core.read_namespaced_pod_log(
                            pod.metadata.name, namespace, container=cs.name, tail_lines=50
                        )
                        for line in log.splitlines():
                            f.write(f"    {line}\n")
                    except Exception as e:
                        f.write(f"    (log read failed: {e})\n")

        # 2. Agent plan-execution failures (active and standby)
        f.write("\n\n=== Latest 'Plan execution failed' per RS member ===\n")
        for rs_name in (ACTIVE_RS_NAME, STANDBY_RS_NAME):
            for i in range(3):
                pod_name = f"{rs_name}-{i}"
                try:
                    log = core.read_namespaced_pod_log(pod_name, namespace, container="mongodb-agent", tail_lines=2000)
                except Exception:
                    continue
                last_failure = None
                for line in log.splitlines():
                    if "Plan execution failed" in line or "AdjustRoles" in line and ".error" in line:
                        last_failure = line
                f.write(f"\n--- {pod_name} ---\n")
                f.write(f"{last_failure or '(none)'}\n")

        # 3. Operator-managed Monarch ConfigMaps
        f.write("\n\n=== Monarch ConfigMaps ===\n")
        cms = core.list_namespaced_config_map(namespace, label_selector="monarch-component")
        for cm in cms.items:
            f.write(f"\n--- {cm.metadata.name} ---\n")
            for k, v in (cm.data or {}).items():
                f.write(f"[{k}]\n{v}\n")

        # 4. Monarch Secret presence (keys only, not values)
        f.write("\n\n=== Monarch Secrets ===\n")
        for rs_name in (ACTIVE_RS_NAME, STANDBY_RS_NAME):
            sec_name = f"{rs_name}-monarch-secrets"
            try:
                sec = core.read_namespaced_secret(sec_name, namespace)
                f.write(f"{sec_name}: keys={list((sec.data or {}).keys())}\n")
            except k8s_client.exceptions.ApiException as e:
                if e.status == 404:
                    f.write(f"{sec_name}: (not present)\n")
                else:
                    f.write(f"{sec_name}: (read failed: {e})\n")

        # 5. CR status conditions (one line each)
        f.write("\n\n=== MongoDB CR status conditions ===\n")
        for rs_name in (ACTIVE_RS_NAME, STANDBY_RS_NAME):
            try:
                cr = custom.get_namespaced_custom_object("mongodb.com", "v1", namespace, "mongodb", rs_name)
            except Exception as e:
                f.write(f"{rs_name}: (read failed: {e})\n")
                continue
            f.write(f"\n--- {rs_name} ---\n")
            f.write(f"phase={cr.get('status', {}).get('phase')}\n")
            for cond in cr.get("status", {}).get("conditions", []):
                f.write(
                    f"  {cond.get('type')}={cond.get('status')} "
                    f"reason={cond.get('reason')} "
                    f"message={cond.get('message', '')[:200]}\n"
                )
    print(f"[monarch-diag] wrote {out_path}")


# ── Item 2: config-key assertion ───────────────────────────────────────────
# Catches "wrong YAML key name" bugs (e.g. securityKeyFile vs
# securityKeyFilePath) immediately in pytest output rather than 5 minutes
# later in a downstream pod-readiness timeout.

# Keys the active shipper's config Secret must contain. The agent's
# oploginjector/maintainer.go and oplogshipper/maintainer.go are the source
# of truth for these field names; if they ever rename anything upstream, this
# assertion fails immediately and points at the exact key.
_SHIPPER_REQUIRED_KEYS = ["srcURI", "backupMongoNodeURI", "mode", "securityKeyFile"]
_INJECTOR_REQUIRED_KEYS = ["srcURI", "backupMongoNodeURI", "securityKeyFile"]


def _assert_monarch_config_keys(namespace: str, rs_name: str, role: str, required_keys: list):
    """Read the operator-rendered config Secret and assert every required key
    appears as a top-level YAML key (one per line, '<key>:' prefix). Fails
    with a precise diff when a key is missing or misspelled."""
    secret_name = f"{rs_name}-monarch-{role}-config"
    core = k8s_client.CoreV1Api()
    secret = core.read_namespaced_secret(secret_name, namespace)
    # Secret data is base64-encoded by Kubernetes
    import base64

    config_bytes = (secret.data or {}).get("config.yaml", b"")
    config = base64.b64decode(config_bytes).decode("utf-8") if config_bytes else ""
    present = [
        line.split(":", 1)[0].strip()
        for line in config.splitlines()
        if line and not line.startswith(" ") and not line.startswith("#") and ":" in line
    ]
    missing = [k for k in required_keys if k not in present]
    assert not missing, (
        f"Monarch {role} config Secret {secret_name} missing required top-level keys: {missing}. "
        f"Present: {present}. Full config:\n{config}"
    )


def _dump_s3_state(namespace: str, test_name: str):
    """List every object under the cluster prefix and dump both DR state files."""
    s3 = _s3_client(_minio_endpoint(namespace))
    # /tmp/diagnostics is the only path copied out of the test pod into EVG artifacts
    # by single_e2e.sh — see kubectl cp .../tmp/diagnostics logs.
    out_dir = "/tmp/diagnostics"
    os.makedirs(out_dir, exist_ok=True)
    out_path = f"{out_dir}/monarch-s3-{test_name}.txt"
    with open(out_path, "w") as f:
        f.write(f"=== S3 listing for s3://{S3_BUCKET}/{CLUSTER_PREFIX}/ ===\n")
        try:
            paginator = s3.get_paginator("list_objects_v2")
            count = 0
            for page in paginator.paginate(Bucket=S3_BUCKET, Prefix=f"{CLUSTER_PREFIX}/"):
                for obj in page.get("Contents", []):
                    f.write(f"{obj['LastModified']!s}  {obj['Size']:>10}  {obj['Key']}\n")
                    count += 1
            f.write(f"\n=== total objects: {count} ===\n")
        except Exception as e:
            f.write(f"(listing failed: {e})\n")

        for cluster_name in (ACTIVE_RS_NAME, STANDBY_RS_NAME):
            f.write(f"\n=== dr_status_{cluster_name}.json ===\n")
            try:
                resp = s3.get_object(Bucket=S3_BUCKET, Key=_dr_state_key(cluster_name))
                content = resp["Body"].read().decode("utf-8")
                # Pretty-print if valid JSON, otherwise dump raw.
                try:
                    f.write(json.dumps(json.loads(content), indent=2) + "\n")
                except Exception:
                    f.write(content + "\n")
            except Exception as e:
                f.write(f"(not present or unreadable: {e})\n")
    print(f"[monarch-diag] wrote {out_path}")


def _dump_monarch_health(namespace: str, test_name: str):
    """Hit the Monarch HTTP health/status endpoints by exec'ing curl in a
    Monarch pod (it has curl baked in) and dump the responses.

    Service DNS for shipper/injector follows the same pattern as the operator's
    GetMonarchServiceDNS: <rs-name>-monarch-<role>-svc.<namespace>.svc.cluster.local"""
    out_dir = "/tmp/diagnostics"
    os.makedirs(out_dir, exist_ok=True)
    out_path = f"{out_dir}/monarch-health-{test_name}.txt"
    apps = k8s_client.AppsV1Api()
    core = k8s_client.CoreV1Api()

    with open(out_path, "w") as f:
        for rs_name in (ACTIVE_RS_NAME, STANDBY_RS_NAME):
            for role in ("shipper", "injector"):
                dep_name = f"{rs_name}-monarch-{role}"
                f.write(f"\n=== {dep_name} ===\n")
                try:
                    dep = apps.read_namespaced_deployment(dep_name, namespace)
                    f.write(
                        f"replicas: desired={dep.spec.replicas} "
                        f"ready={dep.status.ready_replicas} "
                        f"available={dep.status.available_replicas} "
                        f"unavailable={dep.status.unavailable_replicas}\n"
                    )
                except k8s_client.exceptions.ApiException as e:
                    if e.status == 404:
                        f.write("(deployment not present)\n")
                        continue
                    f.write(f"(deployment read failed: {e})\n")
                    continue

                # Find a ready pod and exec curl against the health endpoint.
                pods = core.list_namespaced_pod(namespace, label_selector=f"app=monarch-{role},mongodb={rs_name}")
                f.write(f"pods: {[p.metadata.name for p in pods.items]}\n")
                for pod in pods.items:
                    f.write(f"\n--- {pod.metadata.name} status ---\n")
                    f.write(f"phase: {pod.status.phase}\n")
                    if pod.status.container_statuses:
                        for cs in pod.status.container_statuses:
                            state = "running" if cs.state.running else ("waiting" if cs.state.waiting else "terminated")
                            reason = ""
                            if cs.state.waiting and cs.state.waiting.reason:
                                reason = f" ({cs.state.waiting.reason}: {cs.state.waiting.message})"
                            elif cs.state.terminated and cs.state.terminated.reason:
                                reason = f" ({cs.state.terminated.reason})"
                            f.write(f"  {cs.name}: {state}{reason} restarts={cs.restart_count}\n")
                    # Hit the in-cluster health endpoint by exec'ing curl in the
                    # Monarch pod itself (it has the binary as part of the image).
                    svc_dns = f"{rs_name}-monarch-{role}-svc.{namespace}.svc.cluster.local"
                    for endpoint in (f"http://{svc_dns}:8080/api/v1/status", f"http://{svc_dns}:1122/api/v1/status"):
                        f.write(f"\n--- curl {endpoint} ---\n")
                        try:
                            resp = stream(
                                core.connect_get_namespaced_pod_exec,
                                pod.metadata.name,
                                namespace,
                                command=["curl", "-sS", "--max-time", "5", endpoint],
                                stderr=True,
                                stdin=False,
                                stdout=True,
                                tty=False,
                                _preload_content=True,
                            )
                            f.write(resp or "(empty response)\n")
                        except Exception as e:
                            f.write(f"(exec failed: {e})\n")
                    break  # one pod is enough
    print(f"[monarch-diag] wrote {out_path}")


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
    """Deploy MinIO if not already running, then ensure the bucket exists.

    Uses create_or_replace_from_yaml (python equivalent of kubectl apply) so the
    fixture is idempotent — safe to re-run on test restart without redeploying
    a healthy MinIO instance.
    """
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
def active_rs(namespace: str, ops_manager: MongoDBOpsManager) -> MongoDB:
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-monarch.yaml"), ACTIVE_RS_NAME, namespace)
    resource.set_version(MONARCH_MIN_MDB_VERSION)
    resource.configure(ops_manager, ACTIVE_RS_NAME)
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"
    resource["spec"]["podSpec"] = {
        "podTemplate": {"spec": {"containers": [{"name": "mongodb-agent", "image": AGENT_IMAGE}]}}
    }
    try_load(resource)
    return resource


@fixture(scope="module")
def standby_rs(
    namespace: str,
    active_rs: MongoDB,
    s3_creds_secret: str,
    ops_manager: MongoDBOpsManager,
) -> MongoDB:
    """Standby Clusters always start with Injectors."""
    _wait_for_s3_data(namespace)
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-monarch.yaml"), STANDBY_RS_NAME, namespace)
    resource.set_version(MONARCH_MIN_MDB_VERSION)
    resource["spec"]["monarch"] = _monarch_spec(namespace, "standby")
    resource.configure(ops_manager, STANDBY_RS_NAME)
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"
    resource["spec"]["podSpec"] = {
        "podTemplate": {"spec": {"containers": [{"name": "mongodb-agent", "image": AGENT_IMAGE}]}}
    }
    try_load(resource)
    return resource


@fixture(scope="module")
def active_test_user(active_rs: MongoDB, namespace: str) -> MongoDBUser:
    """SCRAM user for the active RS. Same TEST_USER/TEST_USER_PASSWORD on both
    clusters — they're independent and matching credentials let one pymongo
    client config authenticate against either."""
    user = _create_test_user(active_rs, namespace)
    user.assert_reaches_phase(Phase.Updated, timeout=300)
    return user


@fixture(scope="module")
def standby_test_user(standby_rs: MongoDB, namespace: str) -> MongoDBUser:
    user = _create_test_user(standby_rs, namespace)
    user.assert_reaches_phase(Phase.Updated, timeout=300)
    return user


@fixture(scope="module")
def initialize_inventory_documents(active_rs: MongoDB, active_test_user: MongoDBUser) -> int:
    col = _authed_client(active_rs)[PRODUCTS_DB][INVENTORY_COLLECTION]
    col.delete_many({})
    col.insert_many(INVENTORY_DOCS)
    count = col.count_documents({})
    assert count == len(INVENTORY_DOCS)
    return count


# ══════════════════════════════════════════════════════════════════════════════
# SHIPPER TESTS (Active Cluster)
# ══════════════════════════════════════════════════════════════════════════════


@mark.e2e_replica_set_monarch
class TestMonarchShipper(KubernetesTester):
    """Tests for active cluster with shipper deployment."""

    def test_active_rs_running(self, active_rs: MongoDB):
        """Deploy active RS without Monarch spec first."""
        active_rs.update()
        active_rs.assert_reaches_phase(Phase.Running, timeout=600)

    def test_insert_documents_before_activation(self, initialize_inventory_documents: int):
        """Insert test documents before activating Monarch."""
        assert initialize_inventory_documents == len(INVENTORY_DOCS)

    def test_activate_monarch(self, active_rs: MongoDB, s3_creds_secret: str, namespace: str):
        """Activate Monarch on the running RS by adding spec.monarch.

        Active CRs do not read or seed their own DR state file — the standby
        agent's syncer is the sole consumer. The operator just provisions the
        shipper.
        """
        active_rs["spec"]["monarch"] = _monarch_spec(namespace, "active")
        active_rs.update()
        active_rs.assert_reaches_phase(Phase.Running, timeout=600)
        _wait_for_monarch_condition(active_rs)
        # Operator may report Running before all shipper pods are healthy. Confirm
        # the shipper Deployment has all replicas Ready before downstream tests
        # poll S3 — saves five minutes of misleading "no slices" assertions when
        # the shipper is actually crashlooping.
        _wait_for_deployment_ready(namespace, f"{ACTIVE_RS_NAME}-monarch-shipper", timeout=300)

    def test_shipper_config_has_required_keys(self, active_rs: MongoDB, namespace: str):
        """Fail-fast guardrail against operator/agent YAML key drift.
        Cheaper than waiting for downstream tests to time out when a key is
        renamed (history: securityKeyFilePath vs securityKeyFile)."""
        _assert_monarch_config_keys(namespace, ACTIVE_RS_NAME, "shipper", _SHIPPER_REQUIRED_KEYS)

    def test_automation_config_has_monarch_components(self, active_rs: MongoDB):
        """Verify automation config contains maintainedMonarchComponents for active cluster.

        Note: AWS config fields live under awsConfig (nested), per the schema OM and the
        agent expect after the master merge. See controllers/om/monarch.go AwsStorageConfig.
        """
        config = active_rs.get_automation_config_tester().automation_config
        mc = config["maintainedMonarchComponents"]
        assert len(mc) == 1
        assert mc[0]["replicaSetId"] == ACTIVE_RS_NAME
        assert mc[0]["awsConfig"]["awsBucketName"] == S3_BUCKET
        assert mc[0]["clusterPrefix"] == CLUSTER_PREFIX
        assert mc[0]["initialMode"] == "ACTIVE"

        # Active clusters have shipperConfig with the shipper Service endpoint.
        svc_dns = f"{ACTIVE_RS_NAME}-monarch-shipper-svc.{self.namespace}.svc.cluster.local"
        shipper_shards = mc[0]["shipperConfig"]["shards"]
        assert len(shipper_shards) == 1
        inst = shipper_shards[0]["instances"][0]
        assert inst["hostname"] == svc_dns
        assert inst["healthApiEndpoint"] == f"{svc_dns}:8080"
        assert inst["monarchApiEndpoint"] == f"{svc_dns}:1122"
        assert inst["externallyManaged"] is True

        # injectorConfig is omitted for active clusters (pointer field with omitempty).
        assert "injectorConfig" not in mc[0]

    def test_active_mongod_is_enterprise_build(self, active_rs: MongoDB, active_test_user: MongoDBUser):
        """Monarch's shipper requires `$backupCursor`, an enterprise-only
        aggregation stage. A community mongod accepts the AC and starts up
        but the shipper silently fails to produce snapshots — the bug
        surfaces 10 minutes later as a snapshot-complete-marker timeout.
        Catching it here turns that into a one-second assertion.

        EA injects a custom mongoDbVersions to force enterprise URLs; the
        operator relies on AGENT_IMAGE + MONARCH_MIN_MDB_VERSION instead.
        Either way, what matters is the deployed mongod identifies as
        enterprise.
        """
        info = _authed_client(active_rs).admin.command("buildInfo")
        modules = info.get("modules", [])
        assert "enterprise" in modules, (
            f"Active mongod is not an enterprise build (modules={modules!r}); "
            f"Monarch shipper requires $backupCursor which only exists in enterprise."
        )

    def test_shipper_uploads_to_s3(self, active_rs: MongoDB):
        """Verify shipper is uploading oplog data to S3."""
        _wait_for_s3_data(self.namespace)

    def test_shipper_emits_snapshot_complete_marker(self, active_rs: MongoDB):
        """A fresh `slices/*.bson` listing only proves we're writing oplog frames.
        The reference e2e (verify_s3 in mms-automation) requires a
        `backups/checkpoint_<ts>_v1/complete` marker — that's the signal that
        a snapshot finished and standby bootstrap is safe to attempt."""
        _wait_for_snapshot_complete_marker(self.namespace)

    def test_shipper_ships_new_writes(self, active_rs: MongoDB, active_test_user: MongoDBUser):
        """Verify shipper continues to ship new writes to S3.

        Slices are flushed on a periodic schedule, not per-write. The first slice may take
        tens of seconds to appear after the shipper starts. Poll until we see at least one
        new slice (or until the timeout fires) instead of a fixed sleep.
        """
        s3 = _s3_client(_minio_endpoint(self.namespace))
        prefix = f"{CLUSTER_PREFIX}/{SHARD_ID}/slices/"
        before = s3.list_objects_v2(Bucket=S3_BUCKET, Prefix=prefix).get("KeyCount", 0)
        _authed_client(active_rs)["shipper_test"]["test"].insert_one({"ts": time.time()})

        deadline = time.time() + 300
        after = before
        while time.time() < deadline:
            after = s3.list_objects_v2(Bucket=S3_BUCKET, Prefix=prefix).get("KeyCount", 0)
            if after > before:
                return
            time.sleep(10)
        assert after > before, f"Shipper not shipping: slice count unchanged at {before} after 5 minutes"


# ══════════════════════════════════════════════════════════════════════════════
# INJECTOR TESTS (Standby Cluster)
# ══════════════════════════════════════════════════════════════════════════════


@mark.e2e_replica_set_monarch
class TestMonarchInjector(KubernetesTester):
    """Tests for standby cluster with injector deployment."""

    def test_standby_rs_running(self, standby_rs: MongoDB, namespace: str):
        """Standby RS reaches Running with InjectorReady=True.

        The operator waits for the injector Deployment to be healthy before pushing
        automation config with InjectorInstances. So agents receive InjectorInstances
        pointing to an already-healthy service, and WaitForInjectorReady completes
        immediately. No transient blocking window to test — just final state.

        The operator auto-seeds the standby's DR state file on first reconcile;
        no test-side seeding required.
        """
        standby_rs.update()
        standby_rs.assert_reaches_phase(Phase.Running, timeout=600)
        _wait_for_monarch_condition(standby_rs)  # <-- TODO: a recent changeset broke this
        _wait_for_deployment_ready(namespace, f"{STANDBY_RS_NAME}-monarch-injector", timeout=300)

    def test_standby_fcbis_marker_present(self, standby_rs: MongoDB, namespace: str):
        """Mirrors EA wait_for_fcbis_markers. The agent writes
        `<dbPath>/automation_fcbis_downloaded` after Full Cluster Backup Initial
        Sync completes. Without this, InjectorReady can be True but the standby
        never bootstrapped — verify_replication_ongoing would silently pass on
        write-only data and miss the bootstrap regression.

        Static-arch + Monarch defaults dbPath to /data/db (commit b2c1b0de4),
        so the marker lives at /data/db/automation_fcbis_downloaded inside
        the mongod container. Check at least one member; the agent writes
        the marker per-process after FCBIS, so any member reaching it is
        sufficient evidence that bootstrap completed.
        """
        core = k8s_client.CoreV1Api()
        marker_path = "/data/db/automation_fcbis_downloaded"
        deadline = time.time() + 600
        last_err = None
        while time.time() < deadline:
            for i in range(3):
                pod_name = f"{STANDBY_RS_NAME}-{i}"
                try:
                    # Static architecture runs mongod inside mongodb-agent container
                    resp = stream(
                        core.connect_get_namespaced_pod_exec,
                        pod_name,
                        namespace,
                        container="mongodb-agent",
                        command=["sh", "-c", f"test -f {marker_path} && echo present || echo missing"],
                        stderr=True,
                        stdin=False,
                        stdout=True,
                        tty=False,
                        _preload_content=True,
                    )
                    if (resp or "").strip() == "present":
                        return
                except Exception as e:
                    last_err = repr(e)
            time.sleep(10)
        raise AssertionError(
            f"FCBIS marker {marker_path} not present on any standby member after 600s. " f"Last exec error: {last_err}"
        )

    def test_injector_config_has_required_keys(self, standby_rs: MongoDB, namespace: str):
        """Same guardrail as the shipper-side test; see that one for rationale."""
        _assert_monarch_config_keys(namespace, STANDBY_RS_NAME, "injector", _INJECTOR_REQUIRED_KEYS)

    def test_standby_automation_config(self, standby_rs: MongoDB):
        """Verify automation config has a single InjectorInstance pointing at the K8s Service.

        The injector runs as a Deployment behind a Service. ExternallyManaged=true tells
        the agent not to manage the injector lifecycle. MonarchApiEndpoint routes traffic
        through the Service (bypassing hostname locality matching), so a single instance
        is sufficient — the Service load-balances across injector pods.
        """
        config = standby_rs.get_automation_config_tester().automation_config
        mc = config["maintainedMonarchComponents"]
        assert mc[0]["replicaSetId"] == STANDBY_RS_NAME
        assert mc[0]["initialMode"] == "STANDBY"

        instances = mc[0]["injectorConfig"]["shards"][0]["instances"]
        assert len(instances) == 1

        svc_dns = f"{STANDBY_RS_NAME}-monarch-injector-svc.{self.namespace}.svc.cluster.local"
        inst = instances[0]
        assert inst["hostname"] == svc_dns
        assert inst["healthApiEndpoint"] == f"{svc_dns}:8080"
        assert inst["monarchApiEndpoint"] == f"{svc_dns}:1122"
        assert inst["externallyManaged"] is True

        # shipperConfig is omitted for standby clusters (pointer field with omitempty).
        # Setting mode on an empty ShipperConfig would trigger OM's backupMongoNodeURI
        # validation even when there are no shards to ship.
        assert "shipperConfig" not in mc[0]

    def test_standby_has_no_primary_all_secondary(
        self, standby_rs: MongoDB, standby_test_user: MongoDBUser, namespace: str
    ):
        """Monarch standby invariant: no member is electable, so no PRIMARY is
        ever elected and every mongod member must report SECONDARY with health=1.

        Mirrors verify_standby_replset_health in mms-automation's e2e.py — the
        single load-bearing topology check for a healthy standby. (We do NOT
        assert `member_count == 2× mongods` like the reference does: that
        invariant assumes per-host co-located injectors. Our operator runs a
        single injector Deployment behind a Service, so OM's
        StandbyModificationsSvc adds one injector RS member, not N.)

        Implementation note: connects directly (directConnection=True) to a
        single mongod host to bypass pymongo's server-selection-via-Primary()
        for replSetGetStatus. With a regular MongoClient the standby has no
        primary so the command times out at the topology layer and never
        actually executes.

        The "injector RS member" advertised at <svc>:9995 shows up as
        Unknown/health=0 in rs.status() because it speaks the Monarch
        replication protocol on that port, not the mongod wire protocol —
        we filter it out by stateStr=="SECONDARY" rather than trying to
        whitelist members by name.
        """
        cluster_domain = "cluster.local"
        host = f"{standby_rs.name}-0.{standby_rs.name}-svc.{namespace}.svc.{cluster_domain}"
        uri = f"mongodb://{host}:27017/?directConnection=true"
        client = pymongo.MongoClient(
            uri,
            username=TEST_USER,
            password=TEST_USER_PASSWORD,
            authSource="admin",
            authMechanism="SCRAM-SHA-256",
            serverSelectionTimeoutMs=10000,
        )

        deadline = time.time() + 300
        last_status = None
        last_error = None
        while time.time() < deadline:
            try:
                status = client.admin.command("replSetGetStatus")
                last_status = status
                # Only check mongod members; the injector member runs the Monarch
                # replication protocol on :9995 and surfaces as Unknown over the
                # mongod wire protocol — that's expected, not a topology bug.
                mongod_members = [m for m in status.get("members", []) if ":27017" in m.get("name", "")]
                if mongod_members and all(
                    m.get("stateStr") == "SECONDARY" and m.get("health") == 1 for m in mongod_members
                ):
                    # Also assert no member ever advertised as PRIMARY.
                    assert not any(
                        m.get("stateStr") == "PRIMARY" for m in status.get("members", [])
                    ), f"Standby has a PRIMARY (forbidden by Monarch design): {status['members']}"
                    return
            except pymongo.errors.PyMongoError as e:
                last_error = repr(e)
            time.sleep(5)
        raise AssertionError(
            f"Standby never reached all-SECONDARY/health=1 state.\n"
            f"Last replSetGetStatus: {json.dumps(last_status, default=str) if last_status else '<none>'}\n"
            f"Last pymongo error: {last_error or '<none>'}"
        )

    def test_documents_replicated_to_standby(self, standby_rs: MongoDB, standby_test_user: MongoDBUser):
        """Verify documents from active cluster are replicated to standby."""
        # Standby clusters have no primary (injector advertises as SECONDARY along
        # with the mongod members); reads must target secondaries explicitly.
        col = _authed_client(standby_rs, prefer_secondary=True)[PRODUCTS_DB][INVENTORY_COLLECTION]
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

    def test_ongoing_oplog_replication(
        self,
        active_rs: MongoDB,
        standby_rs: MongoDB,
        active_test_user: MongoDBUser,
        standby_test_user: MongoDBUser,
    ):
        """Sentinel-doc round-trip: write to active, observe on standby.

        Mirrors verify_replication_ongoing in mms-automation's e2e.py — proves
        the active→S3→standby pipeline is alive after FCBIS, not just that it
        bootstrapped once. test_documents_replicated_to_standby only checks
        the initial seed batch, so a shipper that ingests bootstrap and then
        stops shipping would still pass.
        """
        sentinel_db = "monarch_replication_probe"
        sentinel_col = "sentinels"
        sentinel = {"_id": f"sentinel-{time.time()}", "ts": time.time()}

        _authed_client(active_rs)[sentinel_db][sentinel_col].insert_one(sentinel)

        standby_col = _authed_client(standby_rs, prefer_secondary=True)[sentinel_db][sentinel_col]
        deadline = time.time() + 300
        while time.time() < deadline:
            try:
                if standby_col.find_one({"_id": sentinel["_id"]}) is not None:
                    return
            except pymongo.errors.PyMongoError:
                pass
            time.sleep(5)
        raise AssertionError(
            f"Sentinel {sentinel['_id']!r} never appeared on standby within 300s — "
            "ongoing oplog replication is broken (FCBIS-only, no live shipping)"
        )


# ══════════════════════════════════════════════════════════════════════════════
# FAILOVER / PROMOTION TESTS
# ══════════════════════════════════════════════════════════════════════════════


def _wait_for_s3_state(namespace: str, cluster_name: str, expected_state: str, timeout: int = 300):
    """Poll S3 until the DR state file's state field equals expected_state."""
    s3 = _s3_client(_minio_endpoint(namespace))
    key = _dr_state_key(cluster_name)
    deadline = time.time() + timeout
    last_seen = "<missing>"
    while time.time() < deadline:
        try:
            resp = s3.get_object(Bucket=S3_BUCKET, Key=key)
            state = json.loads(resp["Body"].read().decode("utf-8"))
            last_seen = state.get("state", "<missing>")
            if last_seen == expected_state:
                return
        except s3.exceptions.NoSuchKey:
            last_seen = "<NoSuchKey>"
        except Exception:
            pass
        time.sleep(5)
    raise TimeoutError(
        f"S3 DR state for {cluster_name} did not reach {expected_state} after {timeout}s " f"(last seen: {last_seen})"
    )


def _verify_post_promotion_state(self, standby_rs: MongoDB):
    """Common assertions after a promotion completes regardless of trigger source.
    Used by both TestMonarchPromotionViaS3 and TestMonarchPromotionViaCR.

    self is the test class instance (KubernetesTester subclass) so we can read namespace.
    """
    apps = k8s_client.AppsV1Api()

    # ShipperReady=True
    def has_shipper_ready(resource: MongoDB) -> bool:
        try:
            conds = resource["status"].get("conditions", [])
        except (KeyError, TypeError):
            return False
        for cond in conds:
            if cond.get("type") == "ShipperReady" and cond.get("status") == "True":
                return True
        return False

    standby_rs.wait_for(has_shipper_ready, timeout=600, should_raise=True)

    # Injector deleted — both Deployment AND any lingering Pods. K8s analogue of
    # the reference e2e's `pgrep -af 'monarch injector'` empty-on-every-host
    # assertion (step_14): a stuck Pod without its Deployment would still be
    # writing to S3.
    try:
        apps.read_namespaced_deployment(f"{STANDBY_RS_NAME}-monarch-injector", self.namespace)
        assert False, "Injector Deployment should be deleted after promotion"
    except k8s_client.exceptions.ApiException as e:
        assert e.status == 404, f"Expected 404, got {e.status}"

    core = k8s_client.CoreV1Api()
    deadline = time.time() + 60
    leftover = []
    while time.time() < deadline:
        pods = core.list_namespaced_pod(
            self.namespace,
            label_selector=f"app=monarch-injector,mongodb={STANDBY_RS_NAME}",
        )
        leftover = [p.metadata.name for p in pods.items if p.metadata.deletion_timestamp is None]
        if not leftover:
            break
        time.sleep(5)
    assert not leftover, f"Injector pods still running after promotion: {leftover}"

    # Shipper Deployment is up
    _wait_for_deployment_ready(self.namespace, f"{STANDBY_RS_NAME}-monarch-shipper", timeout=180)

    # status.monarch.observedS3State == Active
    def has_observed_active(resource: MongoDB) -> bool:
        try:
            return resource["status"].get("monarch", {}).get("observedS3State", "") == "Active"
        except (KeyError, TypeError):
            return False

    standby_rs.wait_for(has_observed_active, timeout=120, should_raise=True)


# TestMonarchPromotionViaCR exercises the operator-driven promotion path: user
# flips spec.monarch.role to active, the operator writes PromoteStandby to S3
# on the next reconcile (triggerPromotionIfNeeded), then idempotently provisions
# shipper resources. Same end state as a customer doing `aws s3 cp ...
# PromoteStandby` directly — both write the same DR state file, so the CR-trigger
# path subsumes the S3-trigger one. (Single class, NO multi-paragraph docstring:
# KubernetesTester parses class docstrings as YAML and prose with `:` chars
# trips the scanner.)
@mark.e2e_replica_set_monarch
class TestMonarchPromotionViaCR(KubernetesTester):

    # Captured before the CR flip so the post-promotion test can assert the
    # DR-state `version` field advanced monotonically (matches the reference
    # e2e's `version increments monotonically` invariant on dr_status_*.json).
    _pre_trigger_dr_state: dict = None

    def test_trigger_promotion_via_cr(self, standby_rs: MongoDB, namespace: str):
        """User flips spec.monarch.role to active. The operator writes PromoteStandby
        to S3 on the next reconcile (triggerPromotionIfNeeded), then idempotently
        provisions shipper resources."""
        # EA's step_1_preconditions: DR state must be `Standby` before promotion.
        # Operator auto-seeds it on first standby reconcile (per f2c4604e9); this
        # asserts the auto-seed actually happened rather than trusting the docstring.
        pre_state = _read_dr_state_full(namespace, STANDBY_RS_NAME)
        assert pre_state.get("state") == "Standby", (
            f"DR state must be Standby before CR-driven promotion (auto-seed regression?): " f"{pre_state}"
        )
        TestMonarchPromotionViaCR._pre_trigger_dr_state = pre_state
        standby_rs.load()
        standby_rs["spec"]["monarch"]["role"] = "active"
        standby_rs.update()

    def test_operator_writes_promote_standby_to_s3(self, standby_rs: MongoDB, namespace: str):
        """Within seconds of the CR flip the operator should have written
        PromoteStandby to S3, which the agent then advances to Active."""
        _wait_for_s3_state(namespace, STANDBY_RS_NAME, "Active", timeout=600)

    def test_dr_state_schema_and_version_monotonic(self, namespace: str):
        """Final DR state must (1) carry the full schema the agent expects and
        (2) have a strictly-greater `version` than what we observed before the
        trigger. Mirrors the schema in mms-automation's read_dr_state_full /
        write_dr_state and the agent's `standby/s3state.go:S3DRState`."""
        final = _read_dr_state_full(namespace, STANDBY_RS_NAME)
        for field in ("state", "previousState", "clusterName", "version", "lastModified", "schemaVersion"):
            assert field in final, f"DR state missing required field {field!r}: {final}"
        assert final["state"] == "Active"
        assert final["clusterName"] == STANDBY_RS_NAME

        pre = TestMonarchPromotionViaCR._pre_trigger_dr_state
        assert pre is not None, "pre-trigger DR state was not captured"
        # `version` is stored as a string in the JSON but must compare numerically.
        assert int(final["version"]) > int(pre["version"]), (
            f"DR state version did not advance monotonically: "
            f"pre={pre.get('version')!r} final={final.get('version')!r}"
        )

    def test_post_promotion_state(self, standby_rs: MongoDB):
        _verify_post_promotion_state(self, standby_rs)

    def test_no_spec_out_of_sync(self, standby_rs: MongoDB):
        """CR says active, S3 says Active — they agree, no SpecOutOfSync."""
        resource = standby_rs.load()
        conditions = resource["status"].get("conditions", []) if "status" in resource else []
        for cond in conditions:
            if cond.get("type") == "SpecOutOfSync":
                assert (
                    cond.get("status") != "True"
                ), f"SpecOutOfSync should not be True after CR-driven promotion: {cond}"


@mark.e2e_replica_set_monarch
class TestPromotedClusterServingWrites(KubernetesTester):
    """Common post-promotion smoke test: the cluster accepts writes and ships oplogs."""

    def test_promoted_cluster_can_write(self, standby_rs: MongoDB, standby_test_user: MongoDBUser):
        col = _authed_client(standby_rs)["promotion_test"]["writes"]
        col.insert_one({"promoted": True, "ts": time.time()})
        assert col.count_documents({}) >= 1

    def test_promoted_shipper_uploads_to_s3(self, standby_rs: MongoDB, standby_test_user: MongoDBUser):
        s3 = _s3_client(_minio_endpoint(self.namespace))
        prefix = f"{CLUSTER_PREFIX}/{SHARD_ID}/slices/"
        before = s3.list_objects_v2(Bucket=S3_BUCKET, Prefix=prefix).get("KeyCount", 0)
        _authed_client(standby_rs)["shipper_test"]["promoted"].insert_one({"ts": time.time()})

        deadline = time.time() + 300
        after = before
        while time.time() < deadline:
            after = s3.list_objects_v2(Bucket=S3_BUCKET, Prefix=prefix).get("KeyCount", 0)
            if after > before:
                return
            time.sleep(10)
        assert after > before, f"Promoted shipper not shipping: slice count unchanged at {before}"
