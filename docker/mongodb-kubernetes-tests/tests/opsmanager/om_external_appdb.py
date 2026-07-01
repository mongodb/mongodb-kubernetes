import datetime
import time
from typing import Iterator
from urllib.parse import unquote, urlparse

import pymongo
import pytest
from kubetester import create_or_update_secret, try_load
from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import MongoTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pymongo import ReadPreference
from pytest import fixture
from tests import test_logger
from tests.opsmanager.om_ops_manager_backup import create_aws_secret, create_s3_bucket

logger = test_logger.get_test_logger(__name__)

EXT_APPDB_SECRET_NAME = "primary-om-db-ext-connection-string"
EXT_APPDB_SECRET_KEY = "connectionString"

SENTINEL_DB = "sentinel"
SENTINEL_COL = "docs"
SENTINEL_DOC_ID = "pre-migration-marker"

APPDB_S3_SECRET_NAME = "primary-om-db-s3-secret"
APPDB_OPLOG_SECRET_NAME = APPDB_S3_SECRET_NAME + "-oplog"

BACKUP_TEST_DATA = {"_id": "pre-snapshot", "data": "before snapshot"}
POST_SNAPSHOT_DATA = {"_id": "post-snapshot", "data": "after snapshot, oplog-only"}


@fixture(scope="module")
def meta_om(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_meta.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    try_load(resource)
    return resource


@fixture(scope="module")
def primary_om(namespace: str, custom_version: str, custom_appdb_version: str) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_primary.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    try_load(resource)
    return resource


@fixture(scope="module")
def primary_mdb(primary_om: MongoDBOpsManager, namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name="canary-rs",
    )
    resource.configure(primary_om, "development")
    try_load(resource)
    return resource


@fixture(scope="module")
def primary_om_external_appdb(meta_om: MongoDBOpsManager, primary_om: MongoDBOpsManager, namespace: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=primary_om.app_db_name(),
    )
    resource.configure(meta_om, "appdb-project")
    resource.set_version(primary_om["spec"]["applicationDatabase"]["version"])
    # AppDB uses SCRAM; ignoreUnknownUsers keeps pre-existing AppDB users (e.g. mongodb-ops-manager)
    # from being wiped by the generic MongoDB controller's authoritativeSet on first reconcile.
    resource["spec"]["security"] = {
        "authentication": {"enabled": True, "modes": ["SCRAM"], "ignoreUnknownUsers": True}
    }
    try_load(resource)
    return resource


@fixture(scope="module")
def primary_om_external_appdb_user(
    primary_om: MongoDBOpsManager,
    primary_om_external_appdb: MongoDB,
    namespace: str,
) -> MongoDBUser:
    connection_string = primary_om.read_appdb_connection_url()
    password = unquote(urlparse(connection_string).password)
    create_or_update_secret(
        namespace=namespace,
        name="primary-om-db-om-user-password",
        data={"password": password},
    )
    resource = MongoDBUser.from_yaml(
        yaml_fixture("om_external_appdb_ops_manager_user.yaml"), namespace=namespace
    )
    resource["spec"]["mongodbResourceRef"]["name"] = primary_om_external_appdb.name
    try_load(resource)
    return resource


@fixture(scope="module")
def appdb_s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> Iterator[str]:
    create_aws_secret(aws_s3_client, APPDB_S3_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, "test-bucket-appdb-s3")


@fixture(scope="module")
def appdb_oplog_s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> Iterator[str]:
    create_aws_secret(aws_s3_client, APPDB_OPLOG_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, "test-bucket-appdb-oplog")


@fixture(scope="module")
def primary_om_external_appdb_collection(primary_om_external_appdb: MongoDB):
    # module-scoped (not per-function like the backup_restore.py reference): this suite's
    # test classes run strictly in order against a single AppDB, no primary/secondary swap risk.
    collection = pymongo.MongoClient(
        primary_om_external_appdb.tester().cnx_string, **primary_om_external_appdb.tester().default_opts
    )["testdb"]
    return collection["testcollection"].with_options(read_preference=ReadPreference.PRIMARY_PREFERRED)


@pytest.mark.e2e_om_external_appdb
class TestSetup:
    def test_meta_om_created(self, meta_om: MongoDBOpsManager):
        meta_om.update()
        meta_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        meta_om.om_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_primary_om_created(self, primary_om: MongoDBOpsManager):
        primary_om.update()
        primary_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_write_sentinel_to_appdb(self, primary_om: MongoDBOpsManager):
        """Write a sentinel document to the AppDB before STS migration to verify data persistence."""
        tester = MongoTester(primary_om.read_appdb_connection_url(), use_ssl=False)
        tester.client[SENTINEL_DB][SENTINEL_COL].replace_one(
            {"_id": SENTINEL_DOC_ID},
            {"_id": SENTINEL_DOC_ID, "data": "pre-migration"},
            upsert=True,
        )


@pytest.mark.e2e_om_external_appdb
class TestPreSwitchCanary:
    def test_primary_mdb_created(self, primary_mdb: MongoDB):
        primary_mdb.update()
        primary_mdb.assert_reaches_phase(Phase.Running, timeout=600)

    def test_primary_mdb_connectivity(self, primary_mdb: MongoDB):
        primary_mdb.assert_connectivity()


@pytest.mark.e2e_om_external_appdb
class TestSwitchToExternalAppDB:
    def test_create_external_appdb_secret(self, primary_om: MongoDBOpsManager, namespace: str):
        connection_string = primary_om.read_appdb_connection_url()
        create_or_update_secret(
            namespace=namespace,
            name=EXT_APPDB_SECRET_NAME,
            data={EXT_APPDB_SECRET_KEY: connection_string},
        )

    def test_switch_primary_om_to_external_appdb(self, primary_om: MongoDBOpsManager):
        primary_om.load()
        primary_om.set_external_appdb_ref(EXT_APPDB_SECRET_NAME, EXT_APPDB_SECRET_KEY)
        primary_om.update()
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_primary_mdb_still_running_after_switch(self, primary_mdb: MongoDB):
        primary_mdb.reload()
        primary_mdb.assert_reaches_phase(Phase.Running, timeout=300)


@pytest.mark.e2e_om_external_appdb
class TestPostSwitchVerification:
    def test_primary_om_healthy(self, primary_om: MongoDBOpsManager):
        primary_om.get_om_tester().assert_healthiness()

    def test_primary_mdb_connectivity(self, primary_mdb: MongoDB):
        primary_mdb.assert_connectivity()

    def test_sentinel_doc_survives_switch(self, primary_om: MongoDBOpsManager):
        """Verify the sentinel document written before the switch still exists,
        confirming the switch to externalApplicationDatabaseRef didn't lose data."""
        tester = MongoTester(primary_om.read_appdb_connection_url(), use_ssl=False)
        doc = tester.client[SENTINEL_DB][SENTINEL_COL].find_one({"_id": SENTINEL_DOC_ID})
        assert doc is not None, f"Sentinel document '{SENTINEL_DOC_ID}' lost during switch to external AppDB"


@pytest.mark.e2e_om_external_appdb
class TestAppDBTakeover:
    def test_create_ops_manager_user_for_appdb(self, primary_om_external_appdb_user: MongoDBUser):
        """Submit the MongoDBUser for mongodb-ops-manager before the RS so the first automation
        config push includes the user and it is never removed from the AppDB."""
        primary_om_external_appdb_user.update()

    def test_primary_om_external_appdb_created(self, primary_om_external_appdb: MongoDB):
        """Submit the MongoDB resource named 'primary-om-db' against Meta OM.
        The MongoDB controller reconciles the existing AppDB StatefulSet in-place;
        PVCs are re-mounted to the new pods without being deleted or recreated."""
        primary_om_external_appdb.update()
        primary_om_external_appdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_primary_om_external_appdb_user_running(self, primary_om_external_appdb_user: MongoDBUser):
        primary_om_external_appdb_user.assert_reaches_phase(Phase.Running, timeout=300)

    def test_primary_om_external_appdb_connectivity(self, primary_om_external_appdb: MongoDB):
        primary_om_external_appdb.assert_connectivity()


@pytest.mark.e2e_om_external_appdb
class TestFinalVerification:
    def test_primary_om_still_healthy(self, primary_om: MongoDBOpsManager):
        """Primary OM must still be reachable after the MongoDB controller took over the StatefulSet."""
        primary_om.get_om_tester().assert_healthiness()

    def test_sentinel_doc_survives_migration(self, primary_om: MongoDBOpsManager):
        """Verify the sentinel document written before STS migration still exists,
        confirming PVC data was preserved through the takeover."""
        tester = MongoTester(primary_om.read_appdb_connection_url(), use_ssl=False)
        doc = tester.client[SENTINEL_DB][SENTINEL_COL].find_one({"_id": SENTINEL_DOC_ID})
        assert doc is not None, f"Sentinel document '{SENTINEL_DOC_ID}' lost during STS migration"


@pytest.mark.e2e_om_external_appdb
class TestEnableBackupOnAppDB:
    def test_enable_backup_on_meta_om(
        self, meta_om: MongoDBOpsManager, appdb_s3_bucket: str, appdb_oplog_s3_bucket: str
    ):
        meta_om.load()
        meta_om["spec"]["backup"]["enabled"] = True
        meta_om["spec"]["backup"]["s3Stores"] = [
            {
                "name": "appdb-s3-store",
                "s3SecretRef": {"name": APPDB_S3_SECRET_NAME},
                "pathStyleAccessEnabled": True,
                "s3BucketEndpoint": "s3.us-east-1.amazonaws.com",
                "s3BucketName": appdb_s3_bucket,
            }
        ]
        meta_om["spec"]["backup"]["s3OpLogStores"] = [
            {
                "name": "appdb-s3-oplog-store",
                "s3SecretRef": {"name": APPDB_OPLOG_SECRET_NAME},
                "pathStyleAccessEnabled": True,
                "s3BucketEndpoint": "s3.us-east-1.amazonaws.com",
                "s3BucketName": appdb_oplog_s3_bucket,
            }
        ]
        meta_om.update()
        meta_om.backup_status().assert_reaches_phase(Phase.Running, timeout=600)

    def test_enable_backup_on_primary_om_db(self, primary_om_external_appdb: MongoDB):
        primary_om_external_appdb.load()
        primary_om_external_appdb.configure_backup(mode="enabled")
        primary_om_external_appdb.update()
        primary_om_external_appdb.assert_reaches_phase(Phase.Running, timeout=300)
