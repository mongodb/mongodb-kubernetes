"""
e2e test for Monarch Deployment pattern.

Verifies that when a MongoDB CR has spec.monarch configured, the operator creates
a fixed number of Monarch Deployments and Services (default: 3 for redundancy),
and wires the automation config with Service DNS names.

Flow:
  1. Deploy MinIO (S3 store)
  2. Deploy active RS with spec.monarch.role=active, insert docs
  3. Snapshot active RS → MinIO via shipper Job
  4. Deploy standby RS with spec.monarch.role=standby
  5. Verify Monarch Deployments/Services created with correct labels and ports
  6. Verify automation config uses Service DNS names (not localhost)
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
from kubetester import create_or_update_secret, try_load
from kubetester.kubetester import KubernetesTester, run_periodically
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark

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

# Default number of Monarch instances (shippers/injectors) per RS.
# Multiple instances provide redundancy - CAS protocol on S3 ensures safety.
MONARCH_REPLICAS = 3

# ── custom images ───────────────────────────────────────────────────────────
OM_IMAGE = os.getenv(
    "MDB_OM_IMAGE",
    "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-enterprise-ops-manager-ubi:nam.nguyen-om-local",
)
MONARCH_IMAGE = os.getenv("MDB_MONARCH_IMAGE", "quay.io/mongodb/monarch:latest")

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
    active_replica_set.update()
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
    """Verify Monarch Deployment pattern: fixed number of Deployments and Services (default: 3),
    with Service DNS names in the automation config."""

    # ── Phase 1: Active RS with Monarch Deployments ─────────────────────

    def test_active_rs_running(self, active_rs_running: MongoDB):
        assert active_rs_running.get_status_phase() == Phase.Running

    def test_active_monarch_deployments_created(self, active_rs_running: MongoDB):
        """Verify fixed number of Monarch Deployments with app=monarch-shipper label."""
        apps = k8s_client.AppsV1Api()
        deps = apps.list_namespaced_deployment(
            self.namespace,
            label_selector="app=monarch-shipper",
        )
        assert len(deps.items) == MONARCH_REPLICAS, (
            f"Expected {MONARCH_REPLICAS} monarch-shipper Deployments, got {len(deps.items)}"
        )
        dep_names = sorted(d.metadata.name for d in deps.items)
        expected = sorted(f"{ACTIVE_RS_NAME}-monarch-{i}" for i in range(MONARCH_REPLICAS))
        assert dep_names == expected

    def test_active_monarch_deployments_ready(self, active_rs_running: MongoDB):
        apps = k8s_client.AppsV1Api()
        for i in range(MONARCH_REPLICAS):
            dep = apps.read_namespaced_deployment(f"{ACTIVE_RS_NAME}-monarch-{i}", self.namespace)
            assert dep.status.ready_replicas == 1, (
                f"Deployment {dep.metadata.name}: ready_replicas={dep.status.ready_replicas}"
            )

    def test_active_monarch_deployment_containers(self, active_rs_running: MongoDB):
        """Verify Monarch container has correct name, ports, and command."""
        apps = k8s_client.AppsV1Api()
        dep = apps.read_namespaced_deployment(f"{ACTIVE_RS_NAME}-monarch-0", self.namespace)
        containers = dep.spec.template.spec.containers
        assert len(containers) == 1

        c = containers[0]
        assert c.name == "monarch-shipper"

        ports = {p.name: p.container_port for p in c.ports}
        assert ports["health"] == 8080
        assert ports["replication"] == 9995
        assert ports["monarch-api"] == 1122

        command = " ".join(c.command)
        assert "shipper" in command
        assert "--healthApiEndpoint=0.0.0.0:8080" in command
        assert "--monarchApiEndpoint=0.0.0.0:1122" in command

    def test_active_monarch_deployment_labels(self, active_rs_running: MongoDB):
        apps = k8s_client.AppsV1Api()
        dep = apps.read_namespaced_deployment(f"{ACTIVE_RS_NAME}-monarch-0", self.namespace)
        labels = dep.spec.template.metadata.labels
        assert labels["app"] == "monarch-shipper"
        assert labels["mongodb"] == ACTIVE_RS_NAME
        assert labels["monarch-component"] == "shipper"
        assert labels["monarch-index"] == "0"

    def test_active_monarch_services_created(self, active_rs_running: MongoDB):
        """Verify Monarch Services with correct ports and selectors."""
        core = k8s_client.CoreV1Api()
        svcs = core.list_namespaced_service(
            self.namespace,
            label_selector="app=monarch-shipper",
        )
        assert len(svcs.items) == MONARCH_REPLICAS

    def test_active_monarch_service_ports(self, active_rs_running: MongoDB):
        core = k8s_client.CoreV1Api()
        for i in range(MONARCH_REPLICAS):
            svc = core.read_namespaced_service(f"{ACTIVE_RS_NAME}-monarch-{i}-svc", self.namespace)
            port_map = {p.name: p.port for p in svc.spec.ports}
            assert port_map["health"] == 8080
            assert port_map["replication"] == 9995
            assert port_map["monarch-api"] == 1122

            # Verify selector targets the correct Deployment
            assert svc.spec.selector["monarch-component"] == "shipper"
            assert svc.spec.selector["monarch-index"] == str(i)

    def test_active_monarch_owner_references(self, active_rs_running: MongoDB):
        """Monarch Deployments and Services should be owned by the MongoDB resource."""
        apps = k8s_client.AppsV1Api()
        core = k8s_client.CoreV1Api()

        dep = apps.read_namespaced_deployment(f"{ACTIVE_RS_NAME}-monarch-0", self.namespace)
        assert dep.metadata.owner_references is not None
        assert dep.metadata.owner_references[0].kind == "MongoDB"
        assert dep.metadata.owner_references[0].name == ACTIVE_RS_NAME

        svc = core.read_namespaced_service(f"{ACTIVE_RS_NAME}-monarch-0-svc", self.namespace)
        assert svc.metadata.owner_references is not None
        assert svc.metadata.owner_references[0].kind == "MongoDB"
        assert svc.metadata.owner_references[0].name == ACTIVE_RS_NAME

    def test_active_automation_config_monarch_components(self, active_rs_running: MongoDB):
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

    # ── Phase 3: Standby RS with Monarch Deployments ───────────────────

    def test_standby_rs_running(self, standby_replica_set: MongoDB):
        standby_replica_set.update()
        standby_replica_set.assert_reaches_phase(Phase.Running, timeout=600)

    def test_standby_monarch_deployments_created(self, standby_replica_set: MongoDB):
        """Verify Monarch Deployments for standby use app=monarch-injector label."""
        apps = k8s_client.AppsV1Api()
        deps = apps.list_namespaced_deployment(
            self.namespace,
            label_selector=f"app=monarch-injector,mongodb={STANDBY_RS_NAME}",
        )
        assert len(deps.items) == MONARCH_REPLICAS, (
            f"Expected {MONARCH_REPLICAS} monarch-injector Deployments, got {len(deps.items)}"
        )
        dep_names = sorted(d.metadata.name for d in deps.items)
        expected = sorted(f"{STANDBY_RS_NAME}-monarch-{i}" for i in range(MONARCH_REPLICAS))
        assert dep_names == expected

    def test_standby_monarch_deployment_containers(self, standby_replica_set: MongoDB):
        """Standby Deployments should run the injector role."""
        apps = k8s_client.AppsV1Api()
        dep = apps.read_namespaced_deployment(f"{STANDBY_RS_NAME}-monarch-0", self.namespace)
        c = dep.spec.template.spec.containers[0]
        assert c.name == "monarch-injector"
        command = " ".join(c.command)
        assert "injector" in command

    def test_standby_monarch_services_created(self, standby_replica_set: MongoDB):
        core = k8s_client.CoreV1Api()
        svcs = core.list_namespaced_service(
            self.namespace,
            label_selector=f"app=monarch-injector,mongodb={STANDBY_RS_NAME}",
        )
        assert len(svcs.items) == MONARCH_REPLICAS

    def test_standby_monarch_service_ports(self, standby_replica_set: MongoDB):
        core = k8s_client.CoreV1Api()
        for i in range(MONARCH_REPLICAS):
            svc = core.read_namespaced_service(f"{STANDBY_RS_NAME}-monarch-{i}-svc", self.namespace)
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
        """Automation config must use Service DNS names, not localhost."""
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

        instances = shards[0]["instances"]
        members = standby_replica_set["spec"]["members"]
        assert len(instances) == members

        for i, inst in enumerate(instances):
            expected_dns = f"{STANDBY_RS_NAME}-monarch-{i}-svc.{self.namespace}.svc.cluster.local"
            assert inst["hostname"] == expected_dns, (
                f"Instance {i}: expected hostname={expected_dns}, got {inst['hostname']}"
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

    def test_documents_replicated_to_standby(self, standby_replica_set: MongoDB):
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
