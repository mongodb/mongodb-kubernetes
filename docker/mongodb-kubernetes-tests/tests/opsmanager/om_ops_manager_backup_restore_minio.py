import datetime
import os
import time
from typing import List, Optional

import pymongo
from kubetester import (
    create_or_update_namespace,
    create_or_update_secret,
    read_secret,
    try_load,
)
from kubetester.certs import create_mongodb_tls_certs, create_ops_manager_tls_certs
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pymongo import ReadPreference
from pytest import fixture, mark
from tests.common.cert.cert_issuer import create_appdb_certs
from tests.conftest import (
    assert_data_got_restored,
    is_multi_cluster,
)
from tests.opsmanager.conftest import mino_operator_install, mino_tenant_install
from tests.opsmanager.om_ops_manager_backup import S3_SECRET_NAME
from tests.opsmanager.om_ops_manager_backup_tls_custom_ca import (
    FIRST_PROJECT_RS_NAME,
    SECOND_PROJECT_RS_NAME,
)
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

TEST_DATA = {"_id": "unique_id", "name": "John", "address": "Highway 37", "age": 30}

OPLOG_SECRET_NAME = S3_SECRET_NAME + "-oplog"


@fixture(scope="module")
def appdb_certs(namespace: str, issuer: str) -> str:
    return create_appdb_certs(namespace, issuer, "om-backup-db")


@fixture(scope="module")
def ops_manager_certs(namespace: str, issuer: str, tenant_domains: List[str]):
    return create_ops_manager_tls_certs(issuer, namespace, "om-backup", additional_domains=tenant_domains)


# In the helm-chart we hard-code the server secret which contains the secrets for the clients.
# To make it work, we generate these from our existing tls secret and copy them over.
@fixture(scope="module")
def copy_manager_certs_for_minio(namespace: str, ops_manager_certs: str, tenant_name: str) -> str:
    create_or_update_namespace(tenant_name)

    data = read_secret(namespace, ops_manager_certs)
    crt = data["tls.crt"]
    key = data["tls.key"]
    new_data = dict()
    new_data["tls.crt"] = crt
    new_data["tls.key"] = key
    return create_or_update_secret(
        namespace=tenant_name,
        type="kubernetes.io/tls",
        name="tls-ssl-minio",
        data=new_data,
    )


@fixture(scope="module")
def first_project_certs(namespace: str, issuer: str):
    create_mongodb_tls_certs(
        issuer,
        namespace,
        FIRST_PROJECT_RS_NAME,
        f"first-project-{FIRST_PROJECT_RS_NAME}-cert",
    )
    return "first-project"


@fixture(scope="module")
def second_project_certs(namespace: str, issuer: str):
    create_mongodb_tls_certs(
        issuer,
        namespace,
        SECOND_PROJECT_RS_NAME,
        f"second-project-{SECOND_PROJECT_RS_NAME}-cert",
    )
    return "second-project"


@fixture(scope="module")
def tenant_name() -> str:
    return "tenant-0"


@fixture(scope="module")
def tenant_domains() -> List[str]:
    return [
        "tenant-0-pool-0-{0...3}.tenant-0-hl.tenant-0.svc.cluster.local",
        "minio.tenant-0.svc.cluster.local",
        "minio.tenant-0",
        "minio.tenant-0.svc",
        ".tenant-0-hl.tenant-0.svc.cluster.local",
        ".tenant-0.svc.cluster.local",
    ]


@fixture(scope="module")
def s3_bucket_endpoint(tenant_name: str) -> str:
    return f"minio.{tenant_name}.svc.cluster.local"


@fixture(scope="module")
def oplog_s3_bucket_name() -> str:
    return "oplog-s3-bucket"


@fixture(scope="module")
def s3_store_bucket_name() -> str:
    return "s3-store-bucket"


@fixture(scope="module")
def minio_s3_access_key() -> str:
    return "minio"  # this is defined in the values.yaml in the helm-chart


@fixture(scope="module")
def minio_s3_secret_key() -> str:
    return "minio123"  # this is defined in the values.yaml in the helm-chart


@fixture(scope="module")
def ops_manager(
    namespace: str,
    issuer_ca_configmap: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    ops_manager_certs: str,
    appdb_certs: str,
    s3_bucket_endpoint: str,
    s3_store_bucket_name: str,
    minio_s3_access_key: str,
    minio_s3_secret_key: str,
    issuer_ca_filepath: str,
) -> MongoDBOpsManager:
    # ensure the requests library will use this CA when communicating with Ops Manager
    os.environ["REQUESTS_CA_BUNDLE"] = issuer_ca_filepath

    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup_light.yaml"), namespace=namespace
    )

    if try_load(resource):
        return resource

    # these values come from the tenant creation in minio.
    create_or_update_secret(
        namespace,
        S3_SECRET_NAME,
        {
            "accessKey": minio_s3_access_key,
            "secretKey": minio_s3_secret_key,
        },
    )

    create_or_update_secret(
        namespace,
        OPLOG_SECRET_NAME,
        {
            "accessKey": minio_s3_access_key,
            "secretKey": minio_s3_secret_key,
        },
    )

    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()
    resource["spec"]["configuration"]["mms.preflight.run"] = "false"
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_store_bucket_name
    # This ensures that we are using our own customCertificate and not rely on the jvm trusted store
    resource["spec"]["backup"]["s3Stores"][0]["customCertificateSecretRefs"] = [
        {"name": ops_manager_certs, "key": "tls.crt"}
    ]
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketEndpoint"] = s3_bucket_endpoint

    resource["spec"]["security"] = {"tls": {"ca": issuer_ca_configmap, "secretRef": {"name": ops_manager_certs}}}
    resource["spec"]["applicationDatabase"]["security"] = {
        "tls": {"ca": issuer_ca_configmap, "secretRef": {"prefix": appdb_certs}}
    }

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    return resource


@fixture(scope="module")
def mdb_latest(
    ops_manager: MongoDBOpsManager,
    namespace,
    custom_mdb_version: str,
    issuer_ca_configmap: str,
    first_project_certs: str,
):
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=FIRST_PROJECT_RS_NAME,
    ).configure(ops_manager, "mdbLatestProject")
    # MongoD versions greater than 4.2.0 must be enterprise build to enable backup
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.configure_backup(mode="enabled")
    resource.configure_custom_tls(issuer_ca_configmap, first_project_certs)

    try_load(resource)
    return resource


@fixture(scope="module")
def mdb_prev(
    ops_manager: MongoDBOpsManager,
    namespace,
    custom_mdb_prev_version: str,
    issuer_ca_configmap: str,
    second_project_certs: str,
):
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=SECOND_PROJECT_RS_NAME,
    ).configure(ops_manager, "mdbPreviousProject")
    resource.set_version(ensure_ent_version(custom_mdb_prev_version))
    resource.configure_backup(mode="enabled")
    resource.configure_custom_tls(issuer_ca_configmap, second_project_certs)

    try_load(resource)
    return resource


@fixture(scope="function")
def mdb_prev_test_collection(mdb_prev, ca_path: str):
    tester = mdb_prev.tester(ca_path=ca_path, use_ssl=True)

    # we instantiate the pymongo client per test to avoid flakiness as the primary and secondary might swap
    collection = pymongo.MongoClient(tester.cnx_string, **tester.default_opts)["testdb"]
    return collection["testcollection"].with_options(read_preference=ReadPreference.PRIMARY_PREFERRED)


@fixture(scope="function")
def mdb_latest_test_collection(mdb_latest, ca_path: str):
    tester = mdb_latest.tester(ca_path=ca_path, use_ssl=True)

    # we instantiate the pymongo client per test to avoid flakiness as the primary and secondary might swap
    collection = pymongo.MongoClient(tester.cnx_string, **tester.default_opts)["testdb"]
    return collection["testcollection"].with_options(read_preference=ReadPreference.PRIMARY_PREFERRED)


@fixture(scope="module")
def mdb_prev_project(ops_manager: MongoDBOpsManager) -> OMTester:
    return ops_manager.get_om_tester(project_name="mdbPreviousProject")


@fixture(scope="module")
def mdb_latest_project(ops_manager: MongoDBOpsManager) -> OMTester:
    return ops_manager.get_om_tester(project_name="mdbLatestProject")


@mark.e2e_om_ops_manager_backup_restore_minio
class TestMinioCreation:
    def test_install_minio(
        self,
        tenant_name: str,
        issuer_ca_configmap: str,
        copy_manager_certs_for_minio: str,
    ):
        mino_operator_install(namespace=tenant_name)
        mino_tenant_install(namespace=tenant_name)


@mark.e2e_om_ops_manager_backup_restore_minio
class TestOpsManagerCreation:
    def test_create_om(self, ops_manager: MongoDBOpsManager):
        """creates a s3 bucket and an OM resource, the S3 configs get created using AppDB. Oplog store is still required."""
        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            msg_regexp="Oplog Store configuration is required for backup",
            timeout=600,
        )

    def test_s3_oplog_created(
        self,
        ops_manager: MongoDBOpsManager,
        ops_manager_certs: str,
        oplog_s3_bucket_name: str,
        s3_bucket_endpoint: str,
    ):
        ops_manager.load()

        # Backups rely on the JVM default keystore if no customCertificate has been uploaded
        ops_manager["spec"]["backup"]["s3OpLogStores"] = [
            {
                "name": "s3Store2",
                "s3SecretRef": {
                    "name": OPLOG_SECRET_NAME,
                },
                "pathStyleAccessEnabled": True,
                "s3BucketEndpoint": s3_bucket_endpoint,
                "s3BucketName": oplog_s3_bucket_name,
                "customCertificateSecretRefs": [{"name": ops_manager_certs, "key": "tls.crt"}],
            }
        ]

        ops_manager.update()
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running,
            timeout=500,
            ignore_errors=True,
        )

    def test_add_custom_ca_to_project_configmap(
        self, ops_manager: MongoDBOpsManager, issuer_ca_plus: str, namespace: str
    ):
        projects = [
            ops_manager.get_or_create_mongodb_connection_config_map(FIRST_PROJECT_RS_NAME, "firstProject"),
            ops_manager.get_or_create_mongodb_connection_config_map(SECOND_PROJECT_RS_NAME, "secondProject"),
        ]

        data = {
            "sslMMSCAConfigMap": issuer_ca_plus,
        }

        for project in projects:
            KubernetesTester.update_configmap(namespace, project, data)

        # Give a few seconds for the operator to catch the changes on
        # the project ConfigMaps
        time.sleep(10)


@mark.e2e_om_ops_manager_backup_restore_minio
class TestBackupForMongodb:
    """This part ensures that backup for the client works correctly and the snapshot is created.
    Both Mdb 4.0 and 4.2 are tested (as the backup process for them differs significantly)"""

    def test_mdbs_created(self, mdb_latest: MongoDB, mdb_prev: MongoDB):
        mdb_latest.update()
        mdb_prev.update()
        mdb_latest.assert_reaches_phase(Phase.Running)
        mdb_prev.assert_reaches_phase(Phase.Running)

    def test_add_test_data(self, mdb_prev_test_collection, mdb_latest_test_collection):
        mdb_prev_test_collection.insert_one(TEST_DATA)
        mdb_latest_test_collection.insert_one(TEST_DATA)

    def test_mdbs_backed_up(self, mdb_prev_project: OMTester, mdb_latest_project: OMTester):
        # wait until a first snapshot is ready for both
        mdb_prev_project.wait_until_backup_snapshots_are_ready(expected_count=1)
        mdb_latest_project.wait_until_backup_snapshots_are_ready(expected_count=1)


@mark.e2e_om_ops_manager_backup_restore_minio
class TestBackupRestorePIT:
    """This part checks the work of PIT restore."""

    def test_mdbs_change_data(self, mdb_prev_test_collection, mdb_latest_test_collection):
        """Changes the MDB documents to check that restore rollbacks this change later.
        Note, that we need to wait for some time to ensure the PIT timestamp gets to the range
        [snapshot_created <= PIT <= changes_applied]"""
        now_millis = time_to_millis(datetime.datetime.now())
        print("\nCurrent time (millis): {}".format(now_millis))
        time.sleep(30)

        mdb_prev_test_collection.insert_one({"foo": "bar"})
        mdb_latest_test_collection.insert_one({"foo": "bar"})

    def test_mdbs_pit_restore(self, mdb_prev_project: OMTester, mdb_latest_project: OMTester):
        now_millis = time_to_millis(datetime.datetime.now())
        print("\nCurrent time (millis): {}".format(now_millis))

        pit_datetme = datetime.datetime.now() - datetime.timedelta(seconds=15)
        pit_millis = time_to_millis(pit_datetme)
        print("Restoring back to the moment 15 seconds ago (millis): {}".format(pit_millis))

        mdb_prev_project.create_restore_job_pit(pit_millis)
        mdb_latest_project.create_restore_job_pit(pit_millis)

    def test_mdbs_ready(self, mdb_latest: MongoDB, mdb_prev: MongoDB):
        # Note: that we are not waiting for the restore jobs to get finished as PIT restore jobs get FINISHED status
        # right away.
        # But the agent might still do work on the cluster, so we need to wait for that to happen.
        time.sleep(5)
        mdb_latest.assert_reaches_phase(Phase.Running)
        mdb_prev.assert_reaches_phase(Phase.Running)

    def test_data_got_restored(self, mdb_prev_test_collection, mdb_latest_test_collection):
        assert_data_got_restored(TEST_DATA, mdb_prev_test_collection, mdb_latest_test_collection)


@mark.e2e_om_ops_manager_backup_restore_minio
class TestBackupRestoreFromSnapshot:
    """This part tests the restore to the snapshot built once the backup has been enabled."""

    def test_mdbs_change_data(self, mdb_prev_test_collection, mdb_latest_test_collection):
        """Changes the MDB documents to check that restore rollbacks this change later"""
        mdb_prev_test_collection.delete_many({})
        mdb_prev_test_collection.insert_one({"foo": "bar"})

        mdb_latest_test_collection.delete_many({})
        mdb_latest_test_collection.insert_one({"foo": "bar"})

    def test_mdbs_automated_restore(self, mdb_prev_project: OMTester, mdb_latest_project: OMTester):
        restore_prev_id = mdb_prev_project.create_restore_job_snapshot()
        mdb_prev_project.wait_until_restore_job_is_ready(restore_prev_id)

        restore_latest_id = mdb_latest_project.create_restore_job_snapshot()
        mdb_latest_project.wait_until_restore_job_is_ready(restore_latest_id)

    def test_mdbs_ready(self, mdb_latest: MongoDB, mdb_prev: MongoDB):
        # Note: that we are not waiting for the restore jobs to get finished as PIT restore jobs get FINISHED status
        # right away.
        # But the agent might still do work on the cluster, so we need to wait for that to happen.
        time.sleep(5)
        mdb_latest.assert_reaches_phase(Phase.Running)
        mdb_prev.assert_reaches_phase(Phase.Running)

    def test_data_got_restored(self, mdb_prev_test_collection, mdb_latest_test_collection):
        """The data in the db has been restored to the initial"""
        records = list(mdb_prev_test_collection.find())
        assert records == [TEST_DATA]

        records = list(mdb_latest_test_collection.find())
        assert records == [TEST_DATA]


def time_to_millis(date_time) -> int:
    """https://stackoverflow.com/a/11111177/614239"""
    epoch = datetime.datetime.utcfromtimestamp(0)
    pit_millis = (date_time - epoch).total_seconds() * 1000
    return pit_millis
