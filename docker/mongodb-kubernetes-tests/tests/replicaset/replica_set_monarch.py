"""
e2e test for Monarch Deployment pattern.

Verifies that when a MongoDB CR has spec.monarch configured, the operator creates
a single Monarch Deployment (with multiple replicas for redundancy) and a single
Service, wiring the automation config with the Service DNS name.

Flow:
  1. Deploy MinIO (S3 store)
  2. Deploy active RS with spec.monarch.role=active, insert docs
  3. Snapshot active RS → MinIO via shipper Job
  4. Deploy standby RS with spec.monarch.role=standby
  5. Verify Monarch Deployment/Service created with correct labels and ports
  6. Verify automation config uses Service DNS name (not localhost)
  7. Verify data replication to standby
"""

import json as _json
import os
import subprocess
import textwrap
import time

import boto3
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
SHIPPER_JOB_NAME = "monarch-shipper"
SHIPPER_CONFIGMAP = "monarch-shipper-script"

# ── S3 / Monarch config ────────────────────────────────────────────────────
S3_BUCKET = "monarch-standby-bucket"
CLUSTER_PREFIX = "failoverdemo"
MINIO_USER = "minioadmin"
MINIO_PASSWORD = "minioadmin123"
AWS_REGION = "eu-north-1"
S3_CREDS_SECRET = "monarch-s3-creds"

# Default number of Monarch pod replicas per RS.
# Multiple replicas provide redundancy - CAS protocol on S3 ensures safety.
DEFAULT_MONARCH_REPLICAS = 3

# ── custom images ───────────────────────────────────────────────────────────
# Default to staging ECR with ':monarch' tag.
# Build all images: scripts/dev/build-monarch-images.sh
# Then switch context: make switch context=e2e_static_om80_kind_ubi additional_override=private-context-monarch
#
# Agent image comes from operator context (MDB_AGENT_IMAGE env var).
# OM and Monarch images are configured below for this test.
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


def _active_rs_uri(namespace: str, cluster_domain: str, members: int = 3) -> str:
    svc = f"{ACTIVE_RS_NAME}-svc"
    hosts = [f"{ACTIVE_RS_NAME}-{i}.{svc}.{namespace}.svc.{cluster_domain}:27017" for i in range(members)]
    return f"mongodb://{','.join(hosts)}/?replicaSet={ACTIVE_RS_NAME}"


def _mongosh_exec(namespace: str, pod: str, js: str) -> str:
    result = subprocess.run(
        [
            "kubectl", "exec", "-i", "-n", namespace, pod,
            "-c", "mongodb-agent", "--",
            "mongosh", "--quiet", "--file", "/dev/stdin",
        ],
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


# ── fixtures ────────────────────────────────────────────────────────────────


@fixture(scope="module")
def ops_manager(namespace: str, custom_mdb_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(yaml_fixture("om-monarch.yaml"), namespace=namespace)
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
    subprocess.check_call(["kubectl", "apply", "-n", namespace, "-f", yaml_fixture("minio.yaml")])
    _wait_for_deployment_ready(namespace, MINIO_NAME)

    with _port_forward_minio(namespace) as local_endpoint:
        s3 = _s3_client(local_endpoint)
        try:
            s3.create_bucket(Bucket=S3_BUCKET)
        except Exception:
            pass
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
def active_replica_set(
    namespace: str,
    custom_mdb_version: str,
    minio: str,
    s3_creds_secret: str,
    ops_manager: MongoDBOpsManager,
) -> MongoDB:
    """Deploy active RS with spec.monarch.role=active."""
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-monarch.yaml"), ACTIVE_RS_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"
    resource["spec"]["monarch"]["role"] = "active"
    resource["spec"]["monarch"]["s3BucketEndpoint"] = _minio_endpoint(namespace)
    resource["spec"]["monarch"]["image"] = MONARCH_IMAGE
    resource.configure(ops_manager, ACTIVE_RS_NAME)
    try_load(resource)
    return resource


@fixture(scope="module")
def active_rs_running(active_replica_set: MongoDB) -> MongoDB:
    active_replica_set.assert_reaches_phase(Phase.Running, timeout=400)
    return active_replica_set


@fixture(scope="module")
def active_rs_with_data(namespace: str, active_rs_running: MongoDB) -> int:
    docs_json = _json.dumps(INVENTORY_DOCS)
    js = (
        f"db.getSiblingDB('{PRODUCTS_DB}').{INVENTORY_COLLECTION}.deleteMany({{}});"
        f"db.getSiblingDB('{PRODUCTS_DB}').{INVENTORY_COLLECTION}.insertMany({docs_json});"
        f"print(db.getSiblingDB('{PRODUCTS_DB}').{INVENTORY_COLLECTION}.countDocuments({{}}))"
    )
    count = int(_mongosh_exec(namespace, f"{ACTIVE_RS_NAME}-0", js).split("\n")[-1])
    assert count == len(INVENTORY_DOCS)
    return count


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
                            image=MONARCH_IMAGE,
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
                                k8s_client.V1EnvVar(name="MINIO_ENDPOINT", value=_minio_endpoint(namespace)),
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
    s3_creds_secret: str,
    ops_manager: MongoDBOpsManager,
) -> MongoDB:
    """Deploy standby RS with spec.monarch.role=standby."""
    resource = MongoDB.from_yaml(yaml_fixture("replica-set-monarch.yaml"), STANDBY_RS_NAME, namespace)
    resource.set_version(custom_mdb_version)
    resource["metadata"].setdefault("annotations", {})["mongodb.com/v1.architecture"] = "static"
    resource["spec"]["monarch"]["role"] = "standby"
    resource["spec"]["monarch"]["activeReplicaSetId"] = ACTIVE_RS_NAME
    resource["spec"]["monarch"]["injectorVersion"] = "0.1.1"
    resource["spec"]["monarch"]["s3BucketEndpoint"] = _minio_endpoint(namespace)
    resource["spec"]["monarch"]["image"] = MONARCH_IMAGE
    resource["spec"]["monarch"].pop("shipperVersion", None)
    resource.configure(ops_manager, STANDBY_RS_NAME)
    try_load(resource)
    return resource


# ── test class ──────────────────────────────────────────────────────────────


@mark.e2e_replica_set_monarch
class TestMonarchDeployments(KubernetesTester):
    """Verify Monarch Deployment pattern with single Deployment and Service"""

    # ── Phase 1: Active RS with Monarch Deployments ─────────────────────

    def test_active_rs_running(self, active_replica_set: MongoDB):
        active_replica_set.update()
        active_replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def test_active_monarch_deployment_created(self, active_rs_running: MongoDB):
        """Verify single Monarch Deployment with app=monarch-shipper label and correct replicas."""
        apps = k8s_client.AppsV1Api()
        dep_name = f"{ACTIVE_RS_NAME}-monarch-shipper"
        dep = apps.read_namespaced_deployment(dep_name, self.namespace)
        assert dep is not None, f"Deployment {dep_name} not found"
        assert dep.spec.replicas == DEFAULT_MONARCH_REPLICAS, (
            f"Expected {DEFAULT_MONARCH_REPLICAS} replicas, got {dep.spec.replicas}"
        )

    def test_active_monarch_deployment_ready(self, active_rs_running: MongoDB):
        apps = k8s_client.AppsV1Api()
        dep_name = f"{ACTIVE_RS_NAME}-monarch-shipper"
        dep = apps.read_namespaced_deployment(dep_name, self.namespace)
        assert dep.status.ready_replicas == DEFAULT_MONARCH_REPLICAS, (
            f"Deployment {dep_name}: expected {DEFAULT_MONARCH_REPLICAS} ready replicas, got {dep.status.ready_replicas}"
        )

    def test_active_monarch_deployment_containers(self, active_rs_running: MongoDB):
        """Verify Monarch container has correct name, ports, and command."""
        apps = k8s_client.AppsV1Api()
        dep = apps.read_namespaced_deployment(f"{ACTIVE_RS_NAME}-monarch-shipper", self.namespace)
        containers = dep.spec.template.spec.containers
        assert len(containers) == 1

        c = containers[0]
        assert c.name == "monarch-shipper"

        ports = {p.name: p.container_port for p in c.ports}
        assert ports["health"] == 8080
        assert ports["replication"] == 9995
        assert ports["monarch-api"] == 1122

        # Command uses --config flag to read YAML configuration from ConfigMap
        command = " ".join(c.command)
        assert "shipper" in command
        assert "--config=/etc/monarch/config.yaml" in command

    def test_active_monarch_deployment_labels(self, active_rs_running: MongoDB):
        apps = k8s_client.AppsV1Api()
        dep = apps.read_namespaced_deployment(f"{ACTIVE_RS_NAME}-monarch-shipper", self.namespace)
        labels = dep.spec.template.metadata.labels
        assert labels["app"] == "monarch-shipper"
        assert labels["mongodb"] == ACTIVE_RS_NAME
        assert labels["monarch-component"] == "shipper"

    def test_active_monarch_service_created(self, active_rs_running: MongoDB):
        """Verify single Monarch Service with correct ports and selector."""
        core = k8s_client.CoreV1Api()
        svc_name = f"{ACTIVE_RS_NAME}-monarch-shipper-svc"
        svc = core.read_namespaced_service(svc_name, self.namespace)
        assert svc is not None, f"Service {svc_name} not found"

    def test_active_monarch_service_ports(self, active_rs_running: MongoDB):
        core = k8s_client.CoreV1Api()
        svc = core.read_namespaced_service(f"{ACTIVE_RS_NAME}-monarch-shipper-svc", self.namespace)
        port_map = {p.name: p.port for p in svc.spec.ports}
        assert port_map["health"] == 8080
        assert port_map["replication"] == 9995
        assert port_map["monarch-api"] == 1122

        # Verify selector targets the Deployment pods
        assert svc.spec.selector["monarch-component"] == "shipper"
        assert svc.spec.selector["mongodb"] == ACTIVE_RS_NAME

    def test_active_monarch_owner_references(self, active_rs_running: MongoDB):
        """Monarch Deployment, Service, and ConfigMap should be owned by the MongoDB resource."""
        apps = k8s_client.AppsV1Api()
        core = k8s_client.CoreV1Api()

        dep = apps.read_namespaced_deployment(f"{ACTIVE_RS_NAME}-monarch-shipper", self.namespace)
        assert dep.metadata.owner_references is not None
        assert dep.metadata.owner_references[0].kind == "MongoDB"
        assert dep.metadata.owner_references[0].name == ACTIVE_RS_NAME

        svc = core.read_namespaced_service(f"{ACTIVE_RS_NAME}-monarch-shipper-svc", self.namespace)
        assert svc.metadata.owner_references is not None
        assert svc.metadata.owner_references[0].kind == "MongoDB"
        assert svc.metadata.owner_references[0].name == ACTIVE_RS_NAME

        cm = core.read_namespaced_config_map(f"{ACTIVE_RS_NAME}-monarch-shipper-config", self.namespace)
        assert cm.metadata.owner_references is not None
        assert cm.metadata.owner_references[0].kind == "MongoDB"
        assert cm.metadata.owner_references[0].name == ACTIVE_RS_NAME

    def test_active_automation_config_monarch_components(self):
        """Active cluster: maintainedMonarchComponents with empty shards."""
        config = self.get_automation_config()
        assert "maintainedMonarchComponents" in config
        mc = config["maintainedMonarchComponents"]
        assert len(mc) == 1
        assert mc[0]["replicaSetId"] == ACTIVE_RS_NAME
        assert mc[0]["awsBucketName"] == S3_BUCKET
        assert mc[0]["awsRegion"] == AWS_REGION
        assert mc[0]["clusterPrefix"] == CLUSTER_PREFIX
        # Active clusters have empty shards list
        assert mc[0]["injectorConfig"]["shards"] == []

    # ── Phase 2: Snapshot ───────────────────────────────────────────────

    def test_documents_inserted(self, active_rs_with_data: int):
        assert active_rs_with_data == len(INVENTORY_DOCS)

    def test_snapshot_uploaded(self, monarch_snapshot: None):
        pass

    def test_shipper_continuously_ships_slices(self, monarch_snapshot: None):
        """Verify the shipper Deployment continuously ships oplog slices to S3."""
        with _port_forward_minio(self.namespace) as endpoint:
            s3 = _s3_client(endpoint)
            prefix = f"{CLUSTER_PREFIX}/{ACTIVE_RS_NAME}/slices/"

            # Count slices before
            before = s3.list_objects_v2(Bucket=S3_BUCKET, Prefix=prefix)
            before_count = before.get("KeyCount", 0)

            # Insert a document to trigger oplog activity
            _mongosh_exec(
                self.namespace,
                f"{ACTIVE_RS_NAME}-0",
                "db.getSiblingDB('shipper_test').test.insertOne({ts: new Date()})",
            )

            # Wait for shipper to ship (ships every ~10s)
            time.sleep(15)

            # Count slices after
            after = s3.list_objects_v2(Bucket=S3_BUCKET, Prefix=prefix)
            after_count = after.get("KeyCount", 0)

            assert after_count > before_count, (
                f"Shipper not shipping: slice count unchanged at {before_count}"
            )

    # ── Phase 3: Standby RS with Monarch Deployments ───────────────────

    def test_standby_rs_running(self, standby_replica_set: MongoDB):
        standby_replica_set.update()
        standby_replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def test_standby_monarch_deployment_created(self, standby_replica_set: MongoDB):
        """Verify single Monarch Deployment for standby with app=monarch-injector label."""
        apps = k8s_client.AppsV1Api()
        dep_name = f"{STANDBY_RS_NAME}-monarch-injector"
        dep = apps.read_namespaced_deployment(dep_name, self.namespace)
        assert dep is not None, f"Deployment {dep_name} not found"
        assert dep.spec.replicas == DEFAULT_MONARCH_REPLICAS, (
            f"Expected {DEFAULT_MONARCH_REPLICAS} replicas, got {dep.spec.replicas}"
        )

    def test_standby_monarch_deployment_containers(self, standby_replica_set: MongoDB):
        """Standby Deployment should run the injector role with config file."""
        apps = k8s_client.AppsV1Api()
        dep = apps.read_namespaced_deployment(f"{STANDBY_RS_NAME}-monarch-injector", self.namespace)
        c = dep.spec.template.spec.containers[0]
        assert c.name == "monarch-injector"
        command = " ".join(c.command)
        assert "injector" in command
        assert "--config=/etc/monarch/config.yaml" in command

    def test_standby_monarch_service_created(self, standby_replica_set: MongoDB):
        core = k8s_client.CoreV1Api()
        svc_name = f"{STANDBY_RS_NAME}-monarch-injector-svc"
        svc = core.read_namespaced_service(svc_name, self.namespace)
        assert svc is not None, f"Service {svc_name} not found"

    def test_standby_monarch_service_ports(self, standby_replica_set: MongoDB):
        core = k8s_client.CoreV1Api()
        svc = core.read_namespaced_service(f"{STANDBY_RS_NAME}-monarch-injector-svc", self.namespace)
        port_map = {p.name: p.port for p in svc.spec.ports}
        assert port_map["health"] == 8080
        assert port_map["replication"] == 9995
        assert port_map["monarch-api"] == 1122

    def test_standby_no_sidecar_in_statefulset(self, standby_replica_set: MongoDB):
        """Verify that injector is NOT a sidecar — it's a separate Deployment now."""
        sts = self.appsv1.read_namespaced_stateful_set(STANDBY_RS_NAME, self.namespace)
        container_names = [c.name for c in sts.spec.template.spec.containers]
        assert "monarch-injector" not in container_names, (
            f"monarch-injector should be a Deployment, not a sidecar. Found containers: {container_names}"
        )

    def test_standby_automation_config_uses_service_dns(self, standby_replica_set: MongoDB):
        """Automation config must use Service DNS name, not localhost."""
        config = self.get_automation_config()
        assert "maintainedMonarchComponents" in config
        mc = config["maintainedMonarchComponents"]
        assert len(mc) == 1

        # Should reference the active RS
        assert mc[0]["replicaSetId"] == ACTIVE_RS_NAME

        shards = mc[0]["injectorConfig"]["shards"]
        assert len(shards) == 1
        assert shards[0]["shardId"] == "0"
        assert shards[0]["replSetName"] == STANDBY_RS_NAME

        # Single Service fronts all replicas, so automation config has 1 instance
        instances = shards[0]["instances"]
        assert len(instances) == 1, f"Expected 1 instance (single service), got {len(instances)}"

        expected_dns = f"{STANDBY_RS_NAME}-monarch-injector-svc.{self.namespace}.svc.cluster.local"
        inst = instances[0]
        assert inst["hostname"] == expected_dns, (
            f"Expected hostname={expected_dns}, got {inst['hostname']}"
        )
        assert inst["healthApiEndpoint"] == f"{expected_dns}:8080"
        assert inst["monarchApiEndpoint"] == f"{expected_dns}:1122"
        assert inst["port"] == 9995
        assert inst["externallyManaged"] is True
        assert "localhost" not in inst["hostname"]

    def test_standby_injector_member_in_rs_config(self, standby_replica_set: MongoDB):
        """The RS config should contain an injector member with votes=1, priority=1,
        and a processType=INJECTOR tag."""
        config = self.get_automation_config()
        rs = next(r for r in config["replicaSets"] if r["_id"] == STANDBY_RS_NAME)

        mongod_hosts = {f"{STANDBY_RS_NAME}-{i}" for i in range(standby_replica_set["spec"]["members"])}
        injector_members = [m for m in rs["members"] if m.get("host") not in mongod_hosts]

        assert len(injector_members) >= 1, f"No injector members found in RS config. Members: {rs['members']}"
        inj = injector_members[0]
        assert inj["votes"] == 1
        assert inj["priority"] == 1
        assert inj.get("tags", {}).get("processType") == "INJECTOR"

    # ── Phase 4: Data replication ───────────────────────────────────────

    def test_documents_replicated_to_standby(self):
        """The documents inserted into the active RS must appear on the standby."""
        js = f"print(db.getSiblingDB('{PRODUCTS_DB}').{INVENTORY_COLLECTION}.countDocuments({{}}))"
        deadline = time.time() + 600
        count = 0
        while time.time() < deadline:
            try:
                count = int(_mongosh_exec(self.namespace, f"{STANDBY_RS_NAME}-0", js).split("\n")[-1])
                if count == len(INVENTORY_DOCS):
                    break
            except (subprocess.CalledProcessError, ValueError):
                pass
            time.sleep(5)
        assert count == len(INVENTORY_DOCS), (
            f"Expected {len(INVENTORY_DOCS)} documents on standby, got {count}"
        )
