"""
e2e test for MongoDBStandbyCluster — mirrors the docker compose flow:

  1. Deploy MinIO (S3 store)           ←→  docker-compose-minio.yml
  2. Deploy active RS, insert 10 docs   ←→  shipper-entrypoint.sh (data insertion)
  3. Snapshot active RS → MinIO         ←→  shipper-entrypoint.sh Phase 1
  4. Deploy standby RS + CR             ←→  docker-compose-injector-rs.yml
  5. Wait for oplog injection (Running)
  6. Verify 10 docs on standby RS       ←→  "docker exec agent-1 mongosh … inventory.find()"

Required environment variable:
  INJECTOR_IMAGE   The monarch-injector image (quay.io/mongodb/mongodb-kubernetes-monarch-injector:<version>).
                   Built by the MCK pipeline (build_monarch_injector_image Evergreen task).
                   The monarch binary is baked into the image — no runtime download needed.
                   Used for both the injector sidecar and the snapshot (shipper) job.

The test is skipped if INJECTOR_IMAGE is not set.
"""

import json
import os
import textwrap
import time

import boto3
import pymongo
from botocore.config import Config as BotoConfig
from kubernetes import client as k8s_client
from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_standby_cluster import MongoDBStandbyCluster
from kubetester.mongotester import ReplicaSetTester
from kubetester.phase import Phase
from pytest import fixture, mark, skip

# ── resource names ──────────────────────────────────────────────────────────
ACTIVE_RS_NAME = "monarch-active-rs"
STANDBY_RS_NAME = "monarch-standby-rs"
STANDBY_CLUSTER_NAME = "monarch-standby-cluster"
MINIO_NAME = "monarch-minio"
SHIPPER_JOB_NAME = "monarch-shipper"
SHIPPER_CONFIGMAP = "monarch-shipper-script"

# ── S3 / Monarch config ──────────────────────────────────────────────────────
S3_BUCKET = "monarch-standby-bucket"
CLUSTER_PREFIX = "failoverdemo"
MINIO_USER = "minioadmin"
MINIO_PASSWORD = "minioadmin123"
AWS_REGION = "eu-north-1"
S3_CREDS_SECRET = "monarch-s3-creds"

# ── test data (mirrors shipper-entrypoint.sh) ────────────────────────────────
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

# Single image for both sidecar and shipper job.
# Build: docker build -t monarch-injector:latest ~/projects/mms-automation/docker/injector/
INJECTOR_IMAGE = os.getenv("INJECTOR_IMAGE", "")


# ── helpers ──────────────────────────────────────────────────────────────────


def _minio_endpoint(namespace: str) -> str:
    return f"http://{MINIO_NAME}.{namespace}.svc.cluster.local:9000"


def _active_rs_uri(namespace: str, cluster_domain: str, members: int = 3) -> str:
    svc = f"{ACTIVE_RS_NAME}-svc"
    hosts = [f"{ACTIVE_RS_NAME}-{i}.{svc}.{namespace}.svc.{cluster_domain}:27017" for i in range(members)]
    return f"mongodb://{','.join(hosts)}/?replicaSet={ACTIVE_RS_NAME}"


def _wait_for_deployment_ready(namespace: str, name: str, timeout: int = 120):
    apps = k8s_client.AppsV1Api()
    deadline = time.time() + timeout
    while time.time() < deadline:
        dep = apps.read_namespaced_deployment(name, namespace)
        if dep.status.ready_replicas and dep.status.ready_replicas >= 1:
            return
        time.sleep(3)
    raise TimeoutError(f"Deployment {name} not ready after {timeout}s")


def _wait_for_job_completion(namespace: str, name: str, timeout: int = 300):
    batch = k8s_client.BatchV1Api()
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            job = batch.read_namespaced_job(name, namespace)
        except k8s_client.ApiException as e:
            if e.status == 404:
                time.sleep(3)
                continue
            raise
        if job.status.succeeded and job.status.succeeded >= 1:
            return
        if job.status.failed and job.status.failed >= 3:
            raise RuntimeError(f"Job {name} failed (backoffLimit reached)")
        time.sleep(5)
    raise TimeoutError(f"Job {name} did not complete after {timeout}s")


def _s3_client(namespace: str):
    return boto3.client(
        "s3",
        endpoint_url=_minio_endpoint(namespace),
        aws_access_key_id=MINIO_USER,
        aws_secret_access_key=MINIO_PASSWORD,
        region_name=AWS_REGION,
        config=BotoConfig(signature_version="s3v4"),
    )


# ── fixtures ──────────────────────────────────────────────────────────────────


@fixture(scope="module")
def minio(namespace: str) -> str:
    """Deploy MinIO, create the S3 bucket, and seed the DR state file.

    Mirrors docker-compose-minio.yml + the minio-setup sidecar.
    """
    if not INJECTOR_IMAGE:
        skip("INJECTOR_IMAGE is not set — build it from mms-automation/docker/injector/")

    apps = k8s_client.AppsV1Api()
    core = k8s_client.CoreV1Api()

    dep = k8s_client.V1Deployment(
        metadata=k8s_client.V1ObjectMeta(name=MINIO_NAME, namespace=namespace),
        spec=k8s_client.V1DeploymentSpec(
            replicas=1,
            selector=k8s_client.V1LabelSelector(match_labels={"app": MINIO_NAME}),
            template=k8s_client.V1PodTemplateSpec(
                metadata=k8s_client.V1ObjectMeta(labels={"app": MINIO_NAME}),
                spec=k8s_client.V1PodSpec(
                    containers=[
                        k8s_client.V1Container(
                            name="minio",
                            image="minio/minio:latest",
                            args=["server", "/data", "--console-address", ":9001"],
                            env=[
                                k8s_client.V1EnvVar(name="MINIO_ROOT_USER", value=MINIO_USER),
                                k8s_client.V1EnvVar(name="MINIO_ROOT_PASSWORD", value=MINIO_PASSWORD),
                            ],
                            ports=[k8s_client.V1ContainerPort(container_port=9000)],
                            readiness_probe=k8s_client.V1Probe(
                                http_get=k8s_client.V1HTTPGetAction(path="/minio/health/live", port=9000),
                                initial_delay_seconds=5,
                                period_seconds=5,
                            ),
                        )
                    ]
                ),
            ),
        ),
    )
    try:
        apps.create_namespaced_deployment(namespace, dep)
    except k8s_client.ApiException as e:
        if e.status != 409:
            raise

    svc = k8s_client.V1Service(
        metadata=k8s_client.V1ObjectMeta(name=MINIO_NAME, namespace=namespace),
        spec=k8s_client.V1ServiceSpec(
            selector={"app": MINIO_NAME},
            ports=[k8s_client.V1ServicePort(port=9000, target_port=9000, name="api")],
        ),
    )
    try:
        core.create_namespaced_service(namespace, svc)
    except k8s_client.ApiException as e:
        if e.status != 409:
            raise

    _wait_for_deployment_ready(namespace, MINIO_NAME)

    # Create bucket + seed DR state file (mirrors docker-compose-minio.yml minio-setup sidecar).
    s3 = _s3_client(namespace)
    try:
        s3.create_bucket(Bucket=S3_BUCKET)
    except Exception:
        pass  # bucket already exists on re-run

    dr_state = {
        "state": "Standby",
        "previousState": "",
        "clusterName": STANDBY_RS_NAME,
        "version": "1",
        "lastModified": "2026-01-01T00:00:00Z",
        "schemaVersion": "1",
    }
    s3.put_object(
        Bucket=S3_BUCKET,
        Key=f"{CLUSTER_PREFIX}/dr_status_{STANDBY_RS_NAME}.json",
        Body=json.dumps(dr_state).encode(),
    )

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
def active_replica_set(namespace: str, custom_mdb_version: str, minio: str) -> MongoDB:
    """Deploy the active MongoDB RS (source of truth, mirrors activeRS2 in docker compose)."""
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), ACTIVE_RS_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    try_load(resource)
    return resource


@fixture(scope="module")
def active_rs_with_data(namespace: str, active_replica_set: MongoDB, cluster_domain: str) -> int:
    """Deploy active RS and insert 10 documents.

    Mirrors shipper-entrypoint.sh: insertMany into products.inventory.
    Returns the document count inserted.
    """
    active_replica_set.update()
    active_replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    uri = _active_rs_uri(namespace, cluster_domain, members=3)
    mongo = pymongo.MongoClient(uri, serverSelectionTimeoutMS=30_000)
    db = mongo[PRODUCTS_DB]
    db[INVENTORY_COLLECTION].delete_many({})
    db[INVENTORY_COLLECTION].insert_many(INVENTORY_DOCS)
    count = db[INVENTORY_COLLECTION].count_documents({})
    mongo.close()
    assert count == len(INVENTORY_DOCS)
    return count


# Shipper script run inside INJECTOR_IMAGE.
# The monarch binary is baked into the image by the MCK build pipeline — no download needed.
# We override the ENTRYPOINT to run "monarch shipper" instead of "monarch injector".
_SHIPPER_SCRIPT = textwrap.dedent(
    """\
    #!/bin/bash
    set -e
    /usr/local/bin/monarch --version

    echo "Snapshotting $SHARD_ID -> $MINIO_ENDPOINT/$S3_BUCKET"
    /usr/local/bin/monarch shipper \\
      --mode snapshotterOnly \\
      --clusterPrefix "$CLUSTER_PREFIX" \\
      --shardId "$SHARD_ID" \\
      --srcURI "$SRC_URI" \\
      --aws.authMode staticCredentials \\
      --aws.accessKeyId "$AWS_ACCESS_KEY_ID" \\
      --aws.secretAccessKey "$AWS_SECRET_ACCESS_KEY" \\
      --aws.bucketName "$S3_BUCKET" \\
      --aws.region "$AWS_REGION" \\
      --aws.usePathStyle \\
      --aws.customBaseEndpoint "$MINIO_ENDPOINT" \\
      --logLevel info \\
      --logPath /tmp/shipper.log &
    SNAP_PID=$!

    while true; do
      if ! kill -0 "$SNAP_PID" 2>/dev/null; then
        echo "ERROR: snapshotter exited unexpectedly"; cat /tmp/shipper.log || true; exit 1
      fi
      grep -q "Snapshot created successfully" /tmp/shipper.log 2>/dev/null && break
      sleep 2
    done
    kill "$SNAP_PID" 2>/dev/null; wait "$SNAP_PID" 2>/dev/null || true
    echo "Snapshot complete."
    """
)


@fixture(scope="module")
def monarch_snapshot(
    namespace: str,
    active_rs_with_data: int,
    minio: str,
    cluster_domain: str,
) -> None:
    """Snapshot the active RS and upload to MinIO using INJECTOR_IMAGE.

    Mirrors docker-compose-shipper-rs.yml Phase 1 (snapshotterOnly).
    Uses the same INJECTOR_IMAGE as the sidecar — no separate download URL needed.
    """
    core = k8s_client.CoreV1Api()
    batch = k8s_client.BatchV1Api()

    cm = k8s_client.V1ConfigMap(
        metadata=k8s_client.V1ObjectMeta(name=SHIPPER_CONFIGMAP, namespace=namespace),
        data={"shipper.sh": _SHIPPER_SCRIPT},
    )
    try:
        core.create_namespaced_config_map(namespace, cm)
    except k8s_client.ApiException as e:
        if e.status != 409:
            raise

    src_uri = _active_rs_uri(namespace, cluster_domain, members=3)

    job = k8s_client.V1Job(
        metadata=k8s_client.V1ObjectMeta(name=SHIPPER_JOB_NAME, namespace=namespace),
        spec=k8s_client.V1JobSpec(
            backoff_limit=2,
            template=k8s_client.V1PodTemplateSpec(
                spec=k8s_client.V1PodSpec(
                    restart_policy="Never",
                    containers=[
                        k8s_client.V1Container(
                            name="shipper",
                            # Same image as the injector sidecar — monarch binary is
                            # downloaded by the image; no separate MONARCH_DOWNLOAD_URL needed.
                            image=INJECTOR_IMAGE,
                            command=["/bin/bash", "/scripts/shipper.sh"],
                            env=[
                                k8s_client.V1EnvVar(name="CLUSTER_PREFIX", value=CLUSTER_PREFIX),
                                k8s_client.V1EnvVar(name="SHARD_ID", value=ACTIVE_RS_NAME),
                                k8s_client.V1EnvVar(name="SRC_URI", value=src_uri),
                                k8s_client.V1EnvVar(name="AWS_ACCESS_KEY_ID", value=MINIO_USER),
                                k8s_client.V1EnvVar(name="AWS_SECRET_ACCESS_KEY", value=MINIO_PASSWORD),
                                k8s_client.V1EnvVar(name="S3_BUCKET", value=S3_BUCKET),
                                k8s_client.V1EnvVar(name="AWS_REGION", value=AWS_REGION),
                                k8s_client.V1EnvVar(name="MINIO_ENDPOINT", value=minio),
                            ],
                            volume_mounts=[k8s_client.V1VolumeMount(name="scripts", mount_path="/scripts")],
                        )
                    ],
                    volumes=[
                        k8s_client.V1Volume(
                            name="scripts",
                            config_map=k8s_client.V1ConfigMapVolumeSource(name=SHIPPER_CONFIGMAP, default_mode=0o755),
                        )
                    ],
                )
            ),
        ),
    )
    try:
        batch.create_namespaced_job(namespace, job)
    except k8s_client.ApiException as e:
        if e.status != 409:
            raise

    _wait_for_job_completion(namespace, SHIPPER_JOB_NAME, timeout=300)


@fixture(scope="module")
def standby_replica_set(namespace: str, custom_mdb_version: str, monarch_snapshot: None) -> MongoDB:
    """Deploy the standby MongoDB RS.

    Must use static architecture — the operator rejects non-static RSes for injector sidecar support.
    """
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), STANDBY_RS_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    # Required by the StandbyCluster controller (architectures.IsRunningStaticArchitecture check).
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"
    try_load(resource)
    return resource


@fixture(scope="module")
def standby_cluster(
    namespace: str,
    standby_replica_set: MongoDB,
    s3_creds_secret: str,
    minio: str,
) -> MongoDBStandbyCluster:
    """Deploy the MongoDBStandbyCluster CR."""
    resource = MongoDBStandbyCluster.from_yaml(
        yaml_fixture("replica-set-standby-cluster.yaml"),
        STANDBY_CLUSTER_NAME,
        namespace,
    )
    resource["spec"]["injectorImage"] = INJECTOR_IMAGE
    resource["spec"]["monarch"]["s3BucketEndpoint"] = minio
    try_load(resource)
    return resource


# ── test class ────────────────────────────────────────────────────────────────


@mark.e2e_replica_set_standby_cluster
class TestStandbyCluster(KubernetesTester):
    """Full end-to-end standby cluster test mirroring the docker compose flow."""

    def test_active_rs_running(self, active_replica_set: MongoDB):
        assert active_replica_set.get_status_phase() == Phase.Running

    def test_documents_inserted_into_active_rs(self, active_rs_with_data: int):
        assert active_rs_with_data == len(INVENTORY_DOCS)

    def test_snapshot_uploaded_to_minio(self, monarch_snapshot: None):
        # Fixture waits for the shipper Job to complete; reaching here means snapshot uploaded.
        pass

    def test_standby_rs_running(self, standby_replica_set: MongoDB):
        standby_replica_set.update()
        standby_replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    def test_standby_cluster_reaches_running(self, standby_cluster: MongoDBStandbyCluster):
        """Operator adds injector sidecar, updates OM, injector restores snapshot → Running."""
        standby_cluster.update()
        standby_cluster.assert_reaches_phase(Phase.Running, timeout=600)

    def test_injector_sidecar_in_statefulset(self, namespace: str):
        sts = self.appsv1.read_namespaced_stateful_set(STANDBY_RS_NAME, namespace)
        names = [c.name for c in sts.spec.template.spec.containers]
        assert "monarch-injector" in names, f"monarch-injector not in containers: {names}"

    def test_injector_sidecar_ports(self, namespace: str):
        sts = self.appsv1.read_namespaced_stateful_set(STANDBY_RS_NAME, namespace)
        injector = next(c for c in sts.spec.template.spec.containers if c.name == "monarch-injector")
        port_map = {p.name: p.container_port for p in injector.ports}
        assert port_map.get("health") == 8080
        assert port_map.get("replication") == 9995
        assert port_map.get("monarch-api") == 1122

    def test_automation_config_monarch_components(self):
        config = self.get_automation_config()
        assert "maintainedMonarchComponents" in config
        mc = config["maintainedMonarchComponents"]
        assert len(mc) == 1
        assert mc[0]["replicaSetId"] == STANDBY_RS_NAME
        assert mc[0]["injectorConfig"]["shards"][0]["shardId"] == ACTIVE_RS_NAME
        assert mc[0]["injectorConfig"]["shards"][0]["instances"][0]["externallyManaged"] is True
        assert mc[0]["injectorConfig"]["shards"][0]["instances"][0]["healthApiEndpoint"] == "localhost:8080"

    def test_rs_members_have_zero_votes_and_priority(self):
        config = self.get_automation_config()
        rs = next(r for r in config["replicaSets"] if r["_id"] == STANDBY_RS_NAME)
        mongod_members = [m for m in rs["members"] if m.get("host", "").startswith(STANDBY_RS_NAME)]
        assert len(mongod_members) > 0
        for m in mongod_members:
            assert m["votes"] == 0, f"{m['host']}: votes={m['votes']}"
            assert m["priority"] == 0, f"{m['host']}: priority={m['priority']}"

    def test_injector_member_in_rs_config(self):
        config = self.get_automation_config()
        rs = next(r for r in config["replicaSets"] if r["_id"] == STANDBY_RS_NAME)
        injectors = [m for m in rs["members"] if m.get("host") == "localhost:9995"]
        assert len(injectors) == 1
        assert injectors[0]["votes"] == 1
        assert injectors[0]["priority"] == 1
        assert injectors[0].get("tags", {}).get("processType") == "INJECTOR"

    def test_documents_replicated_to_standby(self, namespace: str, cluster_domain: str):
        """The 10 documents inserted into the active RS must appear on the standby RS.

        Mirrors: docker exec agent-1 mongosh --eval "db.getSiblingDB('products').inventory.find()"
        """
        tester = ReplicaSetTester(STANDBY_RS_NAME, 3, namespace=namespace, cluster_domain=cluster_domain)
        mongo = tester.client()
        deadline = time.time() + 600
        count = 0
        while time.time() < deadline:
            try:
                count = mongo[PRODUCTS_DB][INVENTORY_COLLECTION].count_documents({})
                if count == len(INVENTORY_DOCS):
                    break
            except pymongo.errors.PyMongoError:
                pass
            time.sleep(5)
        mongo.close()
        assert count == len(INVENTORY_DOCS), f"Expected {len(INVENTORY_DOCS)} documents on standby, got {count}"
