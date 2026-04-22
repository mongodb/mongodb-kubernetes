"""
e2e test for MongoDBStandbyCluster — mirrors the docker compose flow:

  1. Deploy MinIO (S3 store)           ←→  docker-compose-minio.yml
  2. Deploy active RS, insert 10 docs   ←→  shipper-entrypoint.sh (data insertion)
  3. Snapshot active RS → MinIO         ←→  shipper-entrypoint.sh Phase 1
  4. Deploy standby RS + CR             ←→  docker-compose-injector-rs.yml
  5. Wait for oplog injection (Running)
  6. Verify 10 docs on standby RS       ←→  "docker exec agent-1 mongosh … inventory.find()"

The injector image is sourced from MDB_MONARCH_INJECTOR_IMAGE, set automatically
by the root-context (scripts/dev/contexts/root-context) via print_operator_env.sh.
To update the injector version, change the hardcoded URLs in docker/monarch-injector/Dockerfile.
"""

import os
import subprocess
import textwrap
import time

import boto3
from botocore.config import Config as BotoConfig
from kubernetes import client as k8s_client
from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_standby_cluster import MongoDBStandbyCluster
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark

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

# ── custom images (hardcoded for dev/staging ECR) ────────────────────────────
# OM image: overrides spec.version-based resolution for the OpsManager pod.
OM_IMAGE = "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-enterprise-ops-manager-ubi:nam.nguyen-om-local"
# Agent image: applied via operator MDB_AGENT_IMAGE env var (set by root-context).
AGENT_IMAGE = "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-agent:nam.nguyen-monarch"

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

INJECTOR_IMAGE = os.getenv("MDB_MONARCH_INJECTOR_IMAGE", "")


# ── helpers ──────────────────────────────────────────────────────────────────


def _minio_endpoint(namespace: str) -> str:
    return f"http://{MINIO_NAME}.{namespace}.svc.cluster.local:9000"


def _active_rs_uri(namespace: str, cluster_domain: str, members: int = 3) -> str:
    """Cluster-internal URI for the active RS — used by in-cluster Jobs (shipper)."""
    svc = f"{ACTIVE_RS_NAME}-svc"
    hosts = [f"{ACTIVE_RS_NAME}-{i}.{svc}.{namespace}.svc.{cluster_domain}:27017" for i in range(members)]
    return f"mongodb://{','.join(hosts)}/?replicaSet={ACTIVE_RS_NAME}"


def _mongosh_exec(namespace: str, pod: str, js: str) -> str:
    """Run JavaScript in mongosh inside the database container via stdin, return stdout stripped."""
    result = subprocess.run(
        ["kubectl", "exec", "-i", "-n", namespace, pod, "-c", "mongodb-enterprise-database",
         "--", "mongosh", "--quiet", "--file", "/dev/stdin"],
        input=js.encode(),
        capture_output=True,
        check=True,
    )
    return result.stdout.decode().strip()


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


def _port_forward_minio(namespace: str, local_port: int = 19000):
    """Context manager: kubectl port-forward MinIO to localhost for boto3 access."""
    import contextlib

    @contextlib.contextmanager
    def _ctx():
        proc = subprocess.Popen(
            ["kubectl", "port-forward", "-n", namespace, f"svc/{MINIO_NAME}", f"{local_port}:9000"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        time.sleep(2)
        try:
            yield f"http://localhost:{local_port}"
        finally:
            proc.terminate()
            proc.wait()

    return _ctx()


# ── fixtures ──────────────────────────────────────────────────────────────────


@fixture(scope="module")
def ops_manager(namespace: str, custom_mdb_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    """Deploy OpsManager with custom ECR images, wait for Running."""
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om-monarch.yaml"), namespace=namespace)
    # Override the OM container image with the custom ECR build.
    resource["spec"]["statefulSet"] = {
        "spec": {
            "template": {
                "spec": {
                    "containers": [
                        {
                            "name": "mongodb-ops-manager",
                            "image": OM_IMAGE,
                        }
                    ]
                }
            }
        }
    }
    resource["spec"]["applicationDatabase"]["version"] = custom_appdb_version
    resource.create_admin_secret()
    resource.update()
    resource.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
    resource.om_status().assert_reaches_phase(Phase.Running, timeout=900)
    return resource


@fixture(scope="module")
def minio(namespace: str) -> str:
    """Deploy MinIO via YAML fixture, create the S3 bucket, and seed the DR state file."""
    subprocess.check_call(["kubectl", "apply", "-n", namespace, "-f", yaml_fixture("minio.yaml")])
    _wait_for_deployment_ready(namespace, MINIO_NAME)

    with _port_forward_minio(namespace) as local_endpoint:
        s3 = _s3_client(local_endpoint)
        try:
            s3.create_bucket(Bucket=S3_BUCKET)
        except Exception:
            pass  # bucket already exists on re-run

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
def active_replica_set(namespace: str, custom_mdb_version: str, minio: str, ops_manager: MongoDBOpsManager) -> MongoDB:
    """Deploy the active MongoDB RS (source of truth)."""
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), ACTIVE_RS_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"
    resource.configure(ops_manager, ACTIVE_RS_NAME)
    try_load(resource)
    return resource


@fixture(scope="module")
def active_rs_with_data(namespace: str, active_replica_set: MongoDB) -> int:
    """Deploy active RS and insert 10 documents.

    Mirrors shipper-entrypoint.sh: insertMany into products.inventory.
    Returns the document count inserted.
    """
    active_replica_set.update()
    active_replica_set.assert_reaches_phase(Phase.Running, timeout=400)

    import json as _json
    docs_json = _json.dumps(INVENTORY_DOCS)
    js = (
        f"db.getSiblingDB('{PRODUCTS_DB}').{INVENTORY_COLLECTION}.deleteMany({{}});"
        f"db.getSiblingDB('{PRODUCTS_DB}').{INVENTORY_COLLECTION}.insertMany({docs_json});"
        f"print(db.getSiblingDB('{PRODUCTS_DB}').{INVENTORY_COLLECTION}.countDocuments({{}}))"
    )
    count = int(_mongosh_exec(namespace, f"{ACTIVE_RS_NAME}-0", js).split("\n")[-1])
    assert count == len(INVENTORY_DOCS)
    return count


# Shipper script: runs continuously (snapshotterOnly mode keeps running after the initial
# snapshot). The test detects completion by polling the S3 bucket rather than waiting for
# the process to exit.
_SHIPPER_SCRIPT = textwrap.dedent(
    """\
    #!/bin/bash
    set -e
    exec /usr/local/bin/monarch shipper \\
      --mode snapshotterOnly \\
      --clusterPrefix "$CLUSTER_PREFIX" \\
      --shardId "$SHARD_ID" \\
      --srcURI "$SRC_URI" \\
      --backupMongoNodeURI "$BACKUP_MONGO_NODE_URI" \\
      --aws.authMode staticCredentials \\
      --aws.accessKeyId "$AWS_ACCESS_KEY_ID" \\
      --aws.secretAccessKey "$AWS_SECRET_ACCESS_KEY" \\
      --aws.bucketName "$S3_BUCKET" \\
      --aws.region "$AWS_REGION" \\
      --aws.usePathStyle \\
      --aws.customBaseEndpoint "$MINIO_ENDPOINT" \\
      --logLevel info
    """
)


def _wait_for_snapshot(namespace: str, timeout: int = 300):
    """Wait until monarch shipper has written snapshot objects to the S3 bucket."""
    with _port_forward_minio(namespace) as local_endpoint:
        s3 = _s3_client(local_endpoint)
        deadline = time.time() + timeout
        while time.time() < deadline:
            resp = s3.list_objects_v2(Bucket=S3_BUCKET, Prefix=f"{CLUSTER_PREFIX}/{ACTIVE_RS_NAME}/")
            if resp.get("KeyCount", 0) > 0:
                return
            time.sleep(5)
    raise TimeoutError(f"No snapshot objects appeared in S3 after {timeout}s")


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
    backup_node_uri = (
        f"mongodb://{ACTIVE_RS_NAME}-0.{ACTIVE_RS_NAME}-svc"
        f".{namespace}.svc.{cluster_domain}:27017/?directConnection=true"
    )

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
                                k8s_client.V1EnvVar(name="BACKUP_MONGO_NODE_URI", value=backup_node_uri),
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
    # Delete any leftover job from a previous run so we always use the current spec.
    try:
        batch.delete_namespaced_job(
            SHIPPER_JOB_NAME, namespace,
            body=k8s_client.V1DeleteOptions(propagation_policy="Foreground"),
        )
        time.sleep(3)
    except k8s_client.ApiException as e:
        if e.status != 404:
            raise

    batch.create_namespaced_job(namespace, job)

    _wait_for_snapshot(namespace, timeout=300)


@fixture(scope="module")
def standby_replica_set(
    namespace: str,
    custom_mdb_version: str,
    monarch_snapshot: None,
    ops_manager: MongoDBOpsManager,
) -> MongoDB:
    """Deploy the standby MongoDB RS.

    Must use static architecture — the operator rejects non-static RSes for injector sidecar support.
    Uses its own OM project (separate from the active RS) to avoid the 1-cluster-per-project limit.
    """
    resource = MongoDB.from_yaml(yaml_fixture("replica-set.yaml"), STANDBY_RS_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = False
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"
    resource.configure(ops_manager, STANDBY_RS_NAME)
    try_load(resource)
    return resource


@fixture(scope="module")
def standby_cluster(
    namespace: str,
    standby_replica_set: MongoDB,
    s3_creds_secret: str,
    minio: str,
    ops_manager: MongoDBOpsManager,
) -> MongoDBStandbyCluster:
    """Deploy the MongoDBStandbyCluster CR."""
    resource = MongoDBStandbyCluster.from_yaml(
        yaml_fixture("replica-set-standby-cluster.yaml"),
        STANDBY_CLUSTER_NAME,
        namespace,
    )
    resource["spec"]["injectorImage"] = INJECTOR_IMAGE
    resource["spec"]["monarch"]["s3BucketEndpoint"] = minio
    # Wire to the same OM project as the standby RS.
    cm_name = ops_manager.get_or_create_mongodb_connection_config_map(STANDBY_RS_NAME, STANDBY_RS_NAME, namespace)
    resource["spec"]["opsManager"]["configMapRef"]["name"] = cm_name
    resource["spec"]["credentials"] = ops_manager.api_key_secret(namespace)
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

    def test_documents_replicated_to_standby(self, namespace: str):
        """The 10 documents inserted into the active RS must appear on the standby RS.

        Mirrors: docker exec agent-1 mongosh --eval "db.getSiblingDB('products').inventory.find()"
        """
        js = f"print(db.getSiblingDB('{PRODUCTS_DB}').{INVENTORY_COLLECTION}.countDocuments({{}}))"
        deadline = time.time() + 600
        count = 0
        while time.time() < deadline:
            try:
                count = int(_mongosh_exec(namespace, f"{STANDBY_RS_NAME}-0", js).split("\n")[-1])
                if count == len(INVENTORY_DOCS):
                    break
            except (subprocess.CalledProcessError, ValueError):
                pass
            time.sleep(5)
        assert count == len(INVENTORY_DOCS), f"Expected {len(INVENTORY_DOCS)} documents on standby, got {count}"
