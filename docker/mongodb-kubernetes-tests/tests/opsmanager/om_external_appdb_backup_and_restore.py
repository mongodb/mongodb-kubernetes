import os
import time
from datetime import datetime
from typing import Iterator, Optional

import pytest
from kubernetes import client as k8s_client
from kubernetes.client.rest import ApiException
from kubetester import (
    create_or_update_configmap,
    create_or_update_secret,
    delete_namespace,
    read_configmap,
    read_namespace,
    try_load,
    wait_until,
)
from kubetester.awss3client import AwsS3Client, s3_endpoint
from kubetester.certs import create_mongodb_tls_certs, create_ops_manager_tls_certs
from kubetester.kubetester import KubernetesTester, create_testing_namespace
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMTester, time_to_millis
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture
from tests.clusterwideoperator.om_multiple import install_database_roles
from tests.conftest import create_issuer, get_central_cluster_client, get_operator_clusterwide
from tests.constants import AWS_REGION
from tests.opsmanager.om_external_appdb_test_helpers import assert_sentinel_doc_present, write_sentinel_doc
from tests.opsmanager.om_ops_manager_backup import create_aws_secret, create_s3_bucket

"""
E2E test for backup and PiT restore of an Ops Manager deployment using an external AppDB,
after a complete namespace wipe of both primary-mongodb and workloads-mongodb.

Architecture (see diagrams/om-backup-restore-initial-state.png):

  Three namespaces on a single cluster, managed by one cluster-scoped MCK operator:
  - management-mongodb: Meta OM (internal AppDB) + Meta OM Backup Metadata DB
  - primary-mongodb:    Primary OM (external AppDB) + Primary OM Backup Metadata DB  [DISASTER TARGET]
  - workloads-mongodb:  Workload MongoDB (managed by Primary OM, backup enabled)     [DISASTER TARGET]

  A real AWS S3 bucket stores snapshots + oplog for all three backed-up deployments.
  Meta OM backs up the external AppDB and the Primary OM Backup Metadata DB.
  Primary OM backs up the Workload MongoDB.

The classes form one continuous story, in order:
  1. TestInitialSetup - Phase 1: Deploy Meta OM, external AppDB, Primary OM, Backup Metadata DBs,
     Workload MongoDB. Configure S3/oplog stores, wait for initial snapshots, write sentinel
     documents to AppDB and Workload MongoDB (after snapshots, so data exists only in oplog).
  2. TestDisaster     - Phase 2: Copy critical secrets (gen-key, agent API key, OM admin) from
     primary-mongodb to management-mongodb, then delete both the primary-mongodb and
     workloads-mongodb namespaces entirely.
  3. TestRestore      - Phase 3: Ordered restore — recreate primary-mongodb namespace + secrets,
     PiT restore AppDB (verify sentinel survived), PiT restore Backup Metadata DB, recreate
     Primary OM, recreate workloads-mongodb namespace + Workload MongoDB CR, PiT restore
     Workload MongoDB (verify sentinel survived), verify backup continuity.
"""

META_OM_NAME = "meta-om"
PRIMARY_OM_NAME = "primary-om"
PRIMARY_OM_APPDB_NAME = f"{PRIMARY_OM_NAME}-db"
WORKLOAD_NAME = "workload-mdb"
META_BACKUP_DB_NAME = "meta-om-backup-meta-db"
PRIMARY_BACKUP_DB_NAME = "primary-om-backup-meta-db"

MGMT_NS = "management-mongodb"
PRIMARY_NS = "primary-mongodb"
WORKLOAD_NS = "workloads-mongodb"

S3_SECRET_NAME = "my-s3-secret"

META_OM_ADMIN_SECRET_NAME = "meta-ops-manager-admin-secret"
PRIMARY_OM_ADMIN_SECRET_NAME = "primary-ops-manager-admin-secret"

CA_CONFIGMAP_NAME = "issuer-ca"
ISSUER_NAME = "ca-issuer"
OM_CERT_PREFIX = "certs"
MDB_CERT_PREFIX = "mdb"

# Secrets that must survive the namespace wipe — copied to management-mongodb before disaster,
# restored after namespace recreation.
CRITICAL_SECRETS = [
    f"{PRIMARY_OM_NAME}-gen-key",
    PRIMARY_OM_ADMIN_SECRET_NAME,
]


@fixture(scope="module")
def default_operator(namespace: str, operator_installation_config: dict) -> Operator:
    """Override conftest's default_operator with a cluster-scoped one (WATCH_NAMESPACE=*).

    We also need to reduce MDB_AUTOMATIC_RECOVERY_BACKOFF_TIME_S so that in case
    TLS secrets are not recoverable, MCK needs to push the Automation Config with new certificates first.
    """
    operator_installation_config["customEnvVars"] = (
        operator_installation_config["customEnvVars"] + r"\&MDB_AUTOMATIC_RECOVERY_BACKOFF_TIME_S=120"
    )
    return get_operator_clusterwide(namespace, operator_installation_config)


@fixture(scope="function")
def mgmt_namespace(
    evergreen_task_id: str,
    operator_installation_config: dict[str, str],
    issuer_ca_filepath: str,
    aws_s3_client: AwsS3Client,
) -> str:
    return _get_or_create_test_namespace(
        namespace_name=MGMT_NS,
        evergreen_task_id=evergreen_task_id,
        operator_installation_config=operator_installation_config,
        issuer_ca_filepath=issuer_ca_filepath,
        aws_s3_client=aws_s3_client,
    )


@fixture(scope="function")
def primary_namespace(
    evergreen_task_id: str,
    operator_installation_config: dict[str, str],
    issuer_ca_filepath: str,
    aws_s3_client: AwsS3Client,
) -> str:
    return _get_or_create_test_namespace(
        namespace_name=PRIMARY_NS,
        evergreen_task_id=evergreen_task_id,
        operator_installation_config=operator_installation_config,
        issuer_ca_filepath=issuer_ca_filepath,
        aws_s3_client=aws_s3_client,
    )


@fixture(scope="function")
def workload_namespace(
    evergreen_task_id: str,
    operator_installation_config: dict[str, str],
    issuer_ca_filepath: str,
    aws_s3_client: AwsS3Client,
) -> str:
    return _get_or_create_test_namespace(
        namespace_name=WORKLOAD_NS,
        evergreen_task_id=evergreen_task_id,
        operator_installation_config=operator_installation_config,
        issuer_ca_filepath=issuer_ca_filepath,
        aws_s3_client=aws_s3_client,
    )


@fixture(scope="module")
def s3_bucket(aws_s3_client: AwsS3Client) -> Iterator[str]:
    yield from create_s3_bucket(aws_s3_client, "ext-appdb-bnr")


@fixture(scope="function")
def meta_om(custom_version: Optional[str], custom_appdb_version: str, mgmt_namespace: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_bnr_meta_om.yaml"), name=META_OM_NAME, namespace=mgmt_namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    try_load(resource)
    return resource


@fixture(scope="function")
def meta_backup_db(mgmt_namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("om_external_appdb_bnr_meta_backup_db.yaml"), name=META_BACKUP_DB_NAME, namespace=mgmt_namespace
    )
    try_load(resource)
    return resource


@fixture(scope="function")
def primary_om_ext_appdb(primary_namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("om_external_appdb_bnr_appdb.yaml"), name=PRIMARY_OM_APPDB_NAME, namespace=primary_namespace
    )
    try_load(resource)
    return resource


@fixture(scope="function")
def primary_om(custom_version: Optional[str], primary_namespace: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_bnr_primary_om.yaml"), name=PRIMARY_OM_NAME, namespace=primary_namespace
    )
    resource.set_version(custom_version)
    try_load(resource)
    return resource


@fixture(scope="function")
def primary_backup_db(primary_namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("om_external_appdb_bnr_primary_backup_db.yaml"),
        name=PRIMARY_BACKUP_DB_NAME,
        namespace=primary_namespace,
    )
    try_load(resource)
    return resource


@fixture(scope="function")
def workload_mdb(workload_namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("om_external_appdb_bnr_workload.yaml"), name=WORKLOAD_NAME, namespace=workload_namespace
    )
    try_load(resource)
    return resource


@pytest.mark.e2e_om_external_appdb_backup_and_restore
class TestInitialSetup:
    """Phase 1: Deploy Meta OM, external AppDB, Primary OM, Backup Metadata DBs, Workload MongoDB."""

    def test_setup_default_namespace(self, namespace: str, issuer_ca_filepath: str, issuer: str, s3_bucket):
        # Create ConfigMap with issuer CA so OM and MDBs can trust the issuer for TLS
        # Install cert-manager issuer, then create TLS certs for OM and MDBs, store in Secrets.
        # Create S3 bucket for backup and oplog stores
        # Set REQUESTS_CA_BUNDLE so the tests methods can talk to OM and MDBs over TLS.

        ca = open(issuer_ca_filepath).read()
        create_or_update_configmap(namespace, CA_CONFIGMAP_NAME, {"ca-pem": ca, "mms-ca.crt": ca})

        os.environ["REQUESTS_CA_BUNDLE"] = issuer_ca_filepath

    def test_create_admin_secret_for_meta_om(self, mgmt_namespace: str):
        _create_admin_secret(mgmt_namespace, META_OM_ADMIN_SECRET_NAME)

    def test_deploy_meta_om(self, meta_om: MongoDBOpsManager, namespace: str, mgmt_namespace: str):
        _configure_om_tls(meta_om)
        meta_om.update()
        meta_om.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
        meta_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)

        _copy_admin_key_secret(meta_om, namespace, mgmt_namespace)

    def test_deploy_meta_om_backup_db(self, meta_om: MongoDBOpsManager, meta_backup_db: MongoDB, mgmt_namespace: str):
        _configure_mdb_tls(meta_backup_db, mgmt_namespace)
        config_map_name = _create_project_config(meta_om, meta_backup_db)
        meta_backup_db["spec"]["opsManager"]["configMapRef"]["name"] = config_map_name
        meta_backup_db["spec"]["credentials"] = meta_om.api_key_secret(mgmt_namespace)
        meta_backup_db.update()
        meta_backup_db.assert_reaches_phase(Phase.Running, timeout=900)

    def test_configure_meta_om_backup_stores(self, meta_om: MongoDBOpsManager, s3_bucket: str, meta_backup_db: MongoDB):
        _configure_backup_stores(meta_om, "metaOplogStore", "metaS3Store", meta_backup_db.name, s3_bucket)
        meta_om.update()
        meta_om.backup_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_deploy_primary_om_external_appdb(
        self, primary_om_ext_appdb: MongoDB, meta_om: MongoDBOpsManager, namespace: str, primary_namespace: str
    ):
        _copy_admin_key_secret(meta_om, namespace, primary_namespace)
        _configure_appdb_tls(primary_om_ext_appdb, primary_namespace)

        config_map_name = _create_project_config(meta_om, primary_om_ext_appdb)
        primary_om_ext_appdb["spec"]["opsManager"]["configMapRef"]["name"] = config_map_name
        primary_om_ext_appdb["spec"]["credentials"] = meta_om.api_key_secret(primary_namespace)
        primary_om_ext_appdb.update()
        primary_om_ext_appdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_create_admin_secret_for_primary_om(self, primary_namespace: str):
        _create_admin_secret(primary_namespace, PRIMARY_OM_ADMIN_SECRET_NAME)

    def test_deploy_primary_om(self, primary_om: MongoDBOpsManager, namespace: str, primary_namespace: str):
        _configure_om_tls(primary_om)
        primary_om.update()
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
        primary_om.appdb_status().assert_reaches_phase(Phase.Disabled, timeout=600)
        _copy_admin_key_secret(primary_om, namespace, primary_namespace)

    def test_deploy_primary_backup_db(
        self,
        primary_backup_db: MongoDB,
        meta_om: MongoDBOpsManager,
        primary_om: MongoDBOpsManager,
        s3_bucket: str,
        primary_namespace: str,
    ):
        """Deploy Primary OM's Backup Metadata DB — in primary-mongodb but managed by Meta OM
        (so Meta OM can back it up) — then configure Primary OM's backup stores."""
        _configure_mdb_tls(primary_backup_db, primary_namespace)
        config_map_name = _create_project_config(meta_om, primary_backup_db)
        primary_backup_db["spec"]["opsManager"]["configMapRef"]["name"] = config_map_name
        primary_backup_db["spec"]["credentials"] = meta_om.api_key_secret(primary_namespace)
        primary_backup_db.update()
        primary_backup_db.assert_reaches_phase(Phase.Running, timeout=900)

        _configure_backup_stores(primary_om, "primaryOplogStore", "primaryS3Store", primary_backup_db.name, s3_bucket)
        primary_om.update()
        primary_om.backup_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_deploy_workload_mdb(
        self, workload_mdb: MongoDB, primary_om: MongoDBOpsManager, namespace: str, workload_namespace: str
    ):
        _copy_admin_key_secret(primary_om, namespace, workload_namespace)
        _configure_mdb_tls(workload_mdb, workload_namespace)

        config_map_name = _create_project_config(primary_om, workload_mdb)
        workload_mdb["spec"]["opsManager"]["configMapRef"]["name"] = config_map_name
        workload_mdb["spec"]["credentials"] = primary_om.api_key_secret(workload_namespace)
        workload_mdb.update()
        workload_mdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_wait_for_snapshots_and_write_sentinels(
        self,
        mgmt_namespace: str,
        meta_om: MongoDBOpsManager,
        primary_om: MongoDBOpsManager,
        primary_om_ext_appdb: MongoDB,
        workload_mdb: MongoDB,
        primary_backup_db: MongoDB,
        issuer_ca_filepath: str,
    ):
        """Wait for the initial snapshot of every backed-up deployment, then write sentinel
        documents so they exist only in the oplog — proving PiT restore replays post-snapshot
        writes."""
        primary_om_ext_appdb_tester = meta_om.get_om_tester(project_name=f"{primary_om_ext_appdb.name}-project")
        primary_om_ext_appdb_tester.wait_until_backup_snapshots_are_ready(expected_count=1)

        workload_tester = primary_om.get_om_tester(project_name=f"{workload_mdb.name}-project")
        workload_tester.wait_until_backup_snapshots_are_ready(expected_count=1)

        primary_om_backup_meta_db_tester = meta_om.get_om_tester(project_name=f"{primary_backup_db.name}-project")
        primary_om_backup_meta_db_tester.wait_until_backup_snapshots_are_ready(expected_count=1)

        write_sentinel_doc(primary_om.read_appdb_connection_url(), tls_ca_file=issuer_ca_filepath)
        write_sentinel_doc(workload_mdb.tester().cnx_string, tls_ca_file=issuer_ca_filepath)
        write_sentinel_doc(primary_backup_db.tester().cnx_string, tls_ca_file=issuer_ca_filepath)

        workload_pit_millis = int(time.time() * 1_000) + 1_000

        # Wait for the workload PIT to be restorable before capturing the primary PIT.
        # The backup agent records oplog coverage asynchronously, so the restorable range
        # lags behind real time. Capturing primary_pit_millis after this wait ensures the
        # backup meta DB at that point has recorded the workload's coverage past workload_pit_millis.
        workload_tester.wait_until_pit_restorable(workload_pit_millis, timeout=300)
        primary_pit_millis = int(time.time() * 1_000) + 60_000

        primary_om_ext_appdb_tester.wait_until_pit_restorable(primary_pit_millis, timeout=300)
        primary_om_backup_meta_db_tester.wait_until_pit_restorable(primary_pit_millis, timeout=300)

        # Persist PiT restore timestamps and cluster IDs in a ConfigMap in management-mongodb
        # so TestRestore can run independently without class variables.
        # The cluster IDs are needed because recreating the CRs after the disaster produces
        # new cluster IDs in OM — the old snapshots are only visible via the original IDs.
        cluster_id = primary_om_ext_appdb_tester.get_backup_cluster_id()
        db_cluster_id = primary_om_backup_meta_db_tester.get_backup_cluster_id()
        workload_cluster_id = workload_tester.get_backup_cluster_id()

        create_or_update_configmap(
            mgmt_namespace,
            "om-external-appdb-bnr-restore-points",
            {
                "primaryPitMillis": str(primary_pit_millis),
                "workloadPitMillis": str(workload_pit_millis),
                "primaryOMExtAppDBClusterId": cluster_id,
                "primaryOMBackupMetaDBClusterId": db_cluster_id,
                "workloadClusterId": workload_cluster_id,
            },
        )

    def test_copy_critical_secrets(self, primary_namespace: str, mgmt_namespace: str):
        """Copy gen-key, agent API key, and admin secret from primary-mongodb to management-mongodb."""
        for secret_name in CRITICAL_SECRETS:
            backup_prefix = f"disaster-backup-{secret_name}"
            _copy_secret_raw(primary_namespace, secret_name, mgmt_namespace, backup_prefix)


@pytest.mark.e2e_om_external_appdb_backup_and_restore
class TestDisaster:
    """Phase 2: Wipe primary-mongodb and workloads-mongodb namespaces."""

    def test_delete_primary_and_workload_namespaces(
        self,
        primary_om: MongoDBOpsManager,
        primary_om_ext_appdb: MongoDB,
        primary_backup_db: MongoDB,
        workload_mdb: MongoDB,
        primary_namespace: str,
        workload_namespace: str,
    ):
        """Delete the primary-mongodb and workloads-mongodb namespaces entirely — simulates complete disaster.

        The operator hardcodes a 4200s terminationGracePeriodSeconds on backup daemon pods, which
        blocks namespace termination for 70+ minutes. To work around this:

        1. Annotate all CRs with mongodb.com/disable-reconciliation=true so the operator stops
           reverting our changes and won't recreate deleted resources.
        2. Orphan-delete all StatefulSets so pods aren't cascade-deleted by the STS controller
           (which would use the pod's baked-in 4200s grace period).
        3. Force-delete all pods with grace_period_seconds=0 using a retry loop — k8s allows
           shortening an existing deletionGracePeriodSeconds, but 409 conflicts require retry.
        4. Delete the namespaces — nothing is stuck, they terminate in seconds.
        """
        for resource in (primary_om, primary_om_ext_appdb, primary_backup_db, workload_mdb):
            if "annotations" not in resource["metadata"]:
                resource["metadata"]["annotations"] = {}
            resource["metadata"]["annotations"]["mongodb.com/disable-reconciliation"] = "true"
            resource.update()

        apps_v1 = k8s_client.AppsV1Api()
        core_v1 = k8s_client.CoreV1Api()

        for ns in (primary_namespace, workload_namespace):
            for sts in apps_v1.list_namespaced_stateful_set(ns).items:
                _orphan_delete_statefulset(apps_v1, ns, sts)

            for pod in core_v1.list_namespaced_pod(ns).items:
                _force_delete_pod(core_v1, ns, pod.metadata.name)

            _force_delete_namespace(ns)


@pytest.mark.e2e_om_external_appdb_backup_and_restore
class TestRestore:
    """Phase 3: Ordered restore — AppDB, Backup Meta DB, Primary OM, Workload MongoDB, verify."""

    @fixture(scope="class")
    def restore_points(self) -> dict[str, str]:
        cm = read_configmap(MGMT_NS, "om-external-appdb-bnr-restore-points")
        return {k: v for k, v in cm.items()}

    def test_restore_secrets(self, primary_namespace: str, mgmt_namespace: str):
        for secret_name in CRITICAL_SECRETS:
            backup_prefix = f"disaster-backup-{secret_name}"
            _copy_secret_raw(mgmt_namespace, backup_prefix, primary_namespace, secret_name)

    def test_deploy_primary_ext_appdb(
        self, meta_om: MongoDBOpsManager, primary_om_ext_appdb: MongoDB, namespace: str, primary_namespace: str
    ):
        _copy_admin_key_secret(meta_om, namespace, primary_namespace)
        _configure_appdb_tls(primary_om_ext_appdb, primary_namespace)

        config_map_name = _create_project_config(meta_om, primary_om_ext_appdb)
        primary_om_ext_appdb["spec"]["opsManager"]["configMapRef"]["name"] = config_map_name
        primary_om_ext_appdb["spec"]["credentials"] = meta_om.api_key_secret(primary_namespace)
        primary_om_ext_appdb.update()
        primary_om_ext_appdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_restore_primary_ext_appdb(
        self, meta_om: MongoDBOpsManager, primary_om_ext_appdb: MongoDB, restore_points: dict[str, str]
    ):
        appdb_tester = meta_om.get_om_tester(project_name=f"{primary_om_ext_appdb.name}-project")
        job_id = appdb_tester.create_restore_job_pit(
            int(restore_points["primaryPitMillis"]), cluster_id=restore_points["primaryOMExtAppDBClusterId"]
        )
        appdb_tester.wait_until_restore_job_is_ready(job_id)

        # PIT restore jobs report FINISHED once OM has prepared the restore files; the agents
        # then download and apply them, during which the deployment leaves the Running phase.
        # The sentinel check in the next test is the real proof the data was restored.
        time.sleep(5)
        primary_om_ext_appdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_deploy_primary_backup_db(
        self, meta_om: MongoDBOpsManager, primary_backup_db: MongoDB, primary_namespace: str
    ):
        _configure_mdb_tls(primary_backup_db, primary_namespace)
        config_map_name = _create_project_config(meta_om, primary_backup_db)
        primary_backup_db["spec"]["opsManager"]["configMapRef"]["name"] = config_map_name
        primary_backup_db["spec"]["credentials"] = meta_om.api_key_secret(primary_namespace)
        primary_backup_db.update()
        primary_backup_db.assert_reaches_phase(Phase.Running, timeout=900)

    def test_restore_primary_backup_db(
        self,
        meta_om: MongoDBOpsManager,
        primary_backup_db: MongoDB,
        primary_namespace: str,
        restore_points: dict[str, str],
    ):
        meta_db_tester = meta_om.get_om_tester(project_name=f"{primary_backup_db.name}-project")
        job_id = meta_db_tester.create_restore_job_pit(
            int(restore_points["primaryPitMillis"]), cluster_id=restore_points["primaryOMBackupMetaDBClusterId"]
        )
        meta_db_tester.wait_until_restore_job_is_ready(job_id)

        # FINISHED ≠ applied (see test_restore_appdb); the sentinel check in the next test is
        # the real proof the data was restored.
        time.sleep(5)
        primary_backup_db.assert_reaches_phase(Phase.Running, timeout=900)

    def test_verify_backup_meta_db_sentinel(self, primary_backup_db: MongoDB, issuer_ca_filepath: str):
        """Verify the sentinel document survived the Backup Metadata DB PiT restore."""
        assert_sentinel_doc_present(primary_backup_db.tester().cnx_string, tls_ca_file=issuer_ca_filepath, timeout=600)

    def test_recreate_primary_om(
        self, primary_om: MongoDBOpsManager, primary_backup_db: MongoDB, s3_bucket: str, primary_namespace: str
    ):
        """Deploy Primary OM with externalApplicationDatabaseRef pointing to the restored AppDB."""
        _configure_om_tls(primary_om)
        _configure_backup_stores(primary_om, "primaryOplogStore", "primaryS3Store", primary_backup_db.name, s3_bucket)
        primary_om.update()
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=1200)
        primary_om.appdb_status().assert_reaches_phase(Phase.Disabled, timeout=600)
        primary_om.backup_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_verify_workload_project_restored(self, primary_om: MongoDBOpsManager, workload_mdb: MongoDB):
        """Verify the Workload MongoDB project is restored in Primary OM."""
        workload_tester = primary_om.get_om_tester(project_name=f"{workload_mdb.name}-project")
        workload_tester.assert_group_exists()

    def test_verify_external_appdb_sentinel(self, primary_om: MongoDBOpsManager, issuer_ca_filepath: str):
        """Verify sentinel document survived the PiT restore."""
        assert_sentinel_doc_present(primary_om.read_appdb_connection_url(), tls_ca_file=issuer_ca_filepath, timeout=600)

    def test_deploy_workload_mdb(
        self,
        primary_om: MongoDBOpsManager,
        workload_mdb: MongoDB,
        workload_namespace: str,
        namespace: str,
        restore_points: dict[str, str],
    ):
        _copy_admin_key_secret(primary_om, namespace, workload_namespace)
        _configure_mdb_tls(workload_mdb, workload_namespace)

        config_map_name = _create_project_config(primary_om, workload_mdb)
        workload_mdb["spec"]["opsManager"]["configMapRef"]["name"] = config_map_name
        workload_mdb["spec"]["credentials"] = primary_om.api_key_secret(workload_namespace)
        workload_mdb.update()
        workload_mdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_restore_workload_mdb(
        self, primary_om: MongoDBOpsManager, workload_mdb: MongoDB, restore_points: dict[str, str]
    ):
        """PiT restore Workload MongoDB data from Primary OM."""
        workload_tester = primary_om.get_om_tester(project_name=f"{workload_mdb.name}-project")
        configs = workload_tester.api_read_backup_configs()
        print(f"Backup configs for {workload_mdb.name}-project: {len(configs)}")
        for cfg in configs:
            print(f"  clusterId={cfg['clusterId']}, status={cfg.get('statusName', 'N/A')}")
            snapshots = workload_tester.api_get_snapshots(cfg["clusterId"])
            print(f"  snapshots: {len(snapshots)}")
            for s in snapshots:
                print(f"    id={s['id']}, complete={s.get('complete')}, created={s['created']['date']}")
            try:
                ranges = workload_tester.api_get_restorable_time_ranges(cfg["clusterId"])
                print(f"  restorable time ranges: {len(ranges)}")
                for r in ranges:
                    print(f"    {r}")
            except Exception as e:
                print(f"  Failed to get restorable time ranges: {e}")

        # Use the latest restorable timestamp instead of the original workload PIT, which
        # may fall outside the range after the backup meta DB PiT restore. The sentinel was
        # written before the range started, so it survives a restore to any point in it.
        cluster_id = restore_points["workloadClusterId"]
        # latest_restorable: list[int] = []
        #
        # def restorable_range_exists() -> bool:
        #     ranges = workload_tester.api_get_restorable_time_ranges(cluster_id)
        #     if not ranges:
        #         return False
        #     end = max(ranges, key=lambda r: r["end"]["time"])["end"]
        #     latest_restorable.append(end["time"] * 1000 + end["increment"])
        #     return True
        #
        # run_periodically(restorable_range_exists, timeout=600)
        job_id = workload_tester.create_restore_job_pit(int(restore_points["workloadClusterId"]), cluster_id=cluster_id)
        workload_tester.wait_until_restore_job_is_ready(job_id)

        # FINISHED ≠ applied (see test_restore_appdb); the sentinel check in the next test is
        # the real proof the data was restored.
        time.sleep(5)
        workload_mdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_verify_workload_sentinel(self, workload_mdb: MongoDB, issuer_ca_filepath: str):
        """Verify sentinel document survived the Workload MongoDB PiT restore."""
        workload_mdb.load()
        workload_cnx = workload_mdb.tester().cnx_string
        assert_sentinel_doc_present(workload_cnx, tls_ca_file=issuer_ca_filepath, timeout=600)


def _create_admin_secret(namespace: str, secret_name: str) -> None:
    """Create the Ops Manager admin secret in the given namespace.

    configure_operator.sh only creates it in the test namespace, so create
    it in the OM namespaces too.
    """
    admin_secret_data = {
        "Username": "jane.doe@example.com",
        "Password": "Passw0rd.",
        "FirstName": "Jane",
        "LastName": "Doe",
    }
    create_or_update_secret(namespace, secret_name, admin_secret_data)


def _get_or_create_test_namespace(
    namespace_name: str,
    evergreen_task_id: str,
    operator_installation_config: dict[str, str],
    issuer_ca_filepath: str,
    aws_s3_client: AwsS3Client,
) -> str:
    """Create a namespace and install the database ServiceAccounts/Roles into it.

    The helm chart installs these only in the operator's own namespace; with a clusterwide
    operator every watched namespace needs them installed explicitly, otherwise StatefulSets
    cannot create any pods. A customer recreating a namespace after a disaster must do the same.
    """
    try:
        namespace = read_namespace(namespace_name)
        return namespace.metadata.name
    except ApiException as err:
        if err.status != 404:
            raise

    ns_name = create_testing_namespace(evergreen_task_id, namespace_name)
    install_database_roles(ns_name, operator_installation_config, api_client=get_central_cluster_client())

    create_issuer(ns_name)
    ca = open(issuer_ca_filepath).read()
    create_or_update_configmap(ns_name, CA_CONFIGMAP_NAME, {"ca-pem": ca, "mms-ca.crt": ca})

    create_aws_secret(aws_s3_client, S3_SECRET_NAME, ns_name)

    return ns_name


def _orphan_delete_statefulset(apps_v1, ns: str, sts):
    try:
        apps_v1.delete_namespaced_stateful_set(
            sts.metadata.name,
            ns,
            propagation_policy="Orphan",
            body=k8s_client.V1DeleteOptions(grace_period_seconds=0),
        )
    except ApiException as e:
        if e.status != 404:
            raise


def _force_delete_pod(core_v1: k8s_client.CoreV1Api, namespace: str, name: str) -> None:
    def pod_is_gone() -> bool:
        try:
            core_v1.delete_namespaced_pod(
                name=name,
                namespace=namespace,
                body=k8s_client.V1DeleteOptions(grace_period_seconds=0, propagation_policy="Background"),
                _request_timeout=10,
            )
        except ApiException as exc:
            if exc.status == 404:
                return True
            if exc.status == 409:
                return False
            raise

        return False

    KubernetesTester.wait_until(pod_is_gone, timeout=60)


def _force_delete_namespace(ns_name: str) -> None:
    def ns_is_gone(ns: str) -> bool:
        try:
            read_namespace(ns)
            return False
        except ApiException as err:
            if err.status == 404:
                return True
            raise

    delete_namespace(ns_name)
    try:
        KubernetesTester.wait_until(lambda n=ns_name: ns_is_gone(n), timeout=30)
        return
    except AssertionError:
        pass

    k8s_client.CoreV1Api().patch_namespace(ns_name, {"metadata": {"finalizers": None}})
    try:
        delete_namespace(ns_name)
    except ApiException as e:
        if e.status != 404:
            raise

    KubernetesTester.wait_until(lambda n=ns_name: ns_is_gone(n), timeout=30)


def _copy_secret_raw(src_ns: str, src_name: str, dst_ns: str, dst_name: str) -> None:
    """Copy a secret verbatim (preserving base64-encoded data) between namespaces.

    Uses the raw k8s data field instead of read_secret/create_or_update_secret,
    which decode/re-encode as UTF-8 and fail on binary values like gen-key.
    """
    core_v1 = k8s_client.CoreV1Api()
    secret = core_v1.read_namespaced_secret(src_name, src_ns)
    try:
        core_v1.delete_namespaced_secret(dst_name, dst_ns)
    except ApiException as e:
        if e.status != 404:
            raise
    core_v1.create_namespaced_secret(
        dst_ns,
        k8s_client.V1Secret(
            metadata=k8s_client.V1ObjectMeta(name=dst_name),
            data=secret.data,
            type=secret.type,
        ),
    )


def _copy_admin_key_secret(om: MongoDBOpsManager, operator_ns: str, target_ns: str) -> str:
    """Copy the OM admin key secret from the operator's namespace to the target namespace.

    The operator creates the admin key secret in its own namespace (not the OM's
    namespace). MongoDB CRs managed by this OM need the secret in their namespace.
    Returns the secret name that api_key_secret would produce.
    """
    secret_name = om.api_key_secret(operator_ns)
    _copy_secret_raw(operator_ns, secret_name, target_ns, secret_name)
    return secret_name


def _configure_backup_stores(
    om: MongoDBOpsManager, oplog_store_name: str, s3_store_name: str, backup_db_name: str, s3_bucket: str
) -> None:
    """Configure S3-backed oplog and snapshot stores with metadata in a dedicated MongoDB CR.

    Both oplog data and snapshot data are stored in S3. Both stores' metadata (block index,
    oplog index, snapshot index) is stored in the given backup_db_name MongoDB CR, not in AppDB.
    """
    om["spec"]["backup"]["s3OpLogStores"] = [
        {
            "name": oplog_store_name,
            "s3SecretRef": {"name": S3_SECRET_NAME},
            "pathStyleAccessEnabled": True,
            "s3BucketEndpoint": s3_endpoint(AWS_REGION),
            "s3BucketName": s3_bucket,
            "mongodbResourceRef": {"name": backup_db_name},
        }
    ]
    om["spec"]["backup"]["s3Stores"] = [
        {
            "name": s3_store_name,
            "s3SecretRef": {"name": S3_SECRET_NAME},
            "pathStyleAccessEnabled": True,
            "s3BucketEndpoint": s3_endpoint(AWS_REGION),
            "s3BucketName": s3_bucket,
            "mongodbResourceRef": {"name": backup_db_name},
        }
    ]


def _configure_om_tls(om: MongoDBOpsManager) -> None:
    """Create TLS certs and configure TLS on an OpsManager CR.

    For OMs with an internal AppDB (spec.applicationDatabase), also creates AppDB certs
    and configures TLS on the applicationDatabase spec. OMs with externalApplicationDatabaseRef
    skip this — their AppDB TLS is configured separately via _configure_appdb_tls.
    """
    create_ops_manager_tls_certs(ISSUER_NAME, om.namespace, om.name, secret_name=f"{OM_CERT_PREFIX}-{om.name}-cert")
    om["spec"]["security"] = {"tls": {"ca": CA_CONFIGMAP_NAME}, "certsSecretPrefix": OM_CERT_PREFIX}

    if "applicationDatabase" in om["spec"]:
        appdb_name = om.app_db_name()
        create_mongodb_tls_certs(ISSUER_NAME, om.namespace, appdb_name, f"appdb-{appdb_name}-cert")
        om["spec"]["applicationDatabase"]["security"] = {
            "certsSecretPrefix": "appdb",
            "tls": {"ca": CA_CONFIGMAP_NAME, "enabled": True},
        }


def _configure_mdb_tls(mdb: MongoDB, ns: str) -> None:
    """Create TLS certs and configure TLS on a MongoDB CR."""
    create_mongodb_tls_certs(ISSUER_NAME, ns, mdb.name, f"{MDB_CERT_PREFIX}-{mdb.name}-cert")
    mdb.configure_custom_tls(CA_CONFIGMAP_NAME, MDB_CERT_PREFIX)


def _configure_appdb_tls(appdb: MongoDB, ns: str) -> None:
    """Create TLS certs and configure TLS on the external AppDB MongoDB CR."""
    create_mongodb_tls_certs(ISSUER_NAME, ns, appdb.name, f"appdb-{appdb.name}-cert")
    appdb.configure_custom_tls(CA_CONFIGMAP_NAME, "appdb")


def _create_project_config(om: MongoDBOpsManager, mdb: MongoDB) -> str:
    """Create the project config ConfigMap with TLS CA reference for the agent to verify OM HTTPS."""
    name = f"{mdb.name}-config"
    base_url = om.om_status().get_url()
    assert base_url is not None, "OpsManager URL must not be None"
    create_or_update_configmap(
        mdb.namespace,
        name,
        {
            "baseUrl": base_url,
            "projectName": f"{mdb.name}-project",
            "sslMMSCAConfigMap": CA_CONFIGMAP_NAME,
            "orgId": "",
        },
    )
    return name
