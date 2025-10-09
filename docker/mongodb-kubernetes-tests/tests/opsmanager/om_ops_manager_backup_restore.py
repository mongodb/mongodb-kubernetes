import datetime
import time
from typing import Optional

import pymongo
from kubetester import try_load
from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pymongo import ReadPreference
from pymongo.errors import ServerSelectionTimeoutError
from pytest import fixture, mark
from tests.conftest import assert_data_got_restored, is_multi_cluster
from tests.opsmanager.om_ops_manager_backup import (
    S3_SECRET_NAME,
    create_aws_secret,
    create_s3_bucket,
)
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

"""
The test checks the backup for MongoDB 4.0 and 4.2, checks that snapshots are built and PIT restore and
restore from snapshot are working.
"""

TEST_DATA = {"_id": "unique_id", "name": "John", "address": "Highway 37", "age": 30}

OPLOG_SECRET_NAME = S3_SECRET_NAME + "-oplog"


@fixture(scope="module")
def s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> str:
    create_aws_secret(aws_s3_client, S3_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, "test-bucket-s3")


@fixture(scope="module")
def oplog_s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> str:
    create_aws_secret(aws_s3_client, OPLOG_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, "test-bucket-oplog")


@fixture(scope="module")
def ops_manager(
    namespace: str,
    s3_bucket: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup_light.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    resource.update()
    return resource


@fixture(scope="module")
def mdb_latest(ops_manager: MongoDBOpsManager, namespace, custom_mdb_version: str):
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name="mdb-latest",
    ).configure(ops_manager, "mdbLatestProject")
    # MongoD versions greater than 4.2.0 must be enterprise build to enable backup
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.configure_backup(mode="enabled")

    try_load(resource)

    return resource


@fixture(scope="module")
def mdb_prev(ops_manager: MongoDBOpsManager, namespace, custom_mdb_prev_version: str):
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name="mdb-previous",
    ).configure(ops_manager, "mdbPreviousProject")
    resource.set_version(ensure_ent_version(custom_mdb_prev_version))
    resource.configure_backup(mode="enabled")

    try_load(resource)
    return resource


@fixture(scope="function")
def mdb_prev_test_collection(mdb_prev):
    # we instantiate the pymongo client per test to avoid flakiness as the primary and secondary might swap
    collection = pymongo.MongoClient(mdb_prev.tester().cnx_string, **mdb_prev.tester().default_opts)["testdb"]
    return collection["testcollection"].with_options(read_preference=ReadPreference.PRIMARY_PREFERRED)


@fixture(scope="function")
def mdb_latest_test_collection(mdb_latest):
    # we instantiate the pymongo client per test to avoid flakiness as the primary and secondary might swap
    collection = pymongo.MongoClient(mdb_latest.tester().cnx_string, **mdb_latest.tester().default_opts)["testdb"]
    return collection["testcollection"].with_options(read_preference=ReadPreference.PRIMARY_PREFERRED)


@fixture(scope="module")
def mdb_prev_project(ops_manager: MongoDBOpsManager) -> OMTester:
    return ops_manager.get_om_tester(project_name="mdbPreviousProject")


@fixture(scope="module")
def mdb_latest_project(ops_manager: MongoDBOpsManager) -> OMTester:
    return ops_manager.get_om_tester(project_name="mdbLatestProject")


@mark.e2e_om_ops_manager_backup_restore
class TestOpsManagerCreation:
    def test_create_om(self, ops_manager: MongoDBOpsManager):
        """creates a s3 bucket and an OM resource, the S3 configs get created using AppDB. Oplog store is still required."""
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            msg_regexp="Oplog Store configuration is required for backup",
            timeout=300,
        )

    def test_s3_oplog_created(self, ops_manager: MongoDBOpsManager, oplog_s3_bucket: str):
        ops_manager.load()

        ops_manager["spec"]["backup"]["s3OpLogStores"] = [
            {
                "name": "s3Store2",
                "s3SecretRef": {
                    "name": OPLOG_SECRET_NAME,
                },
                "pathStyleAccessEnabled": True,
                "s3BucketEndpoint": "s3.us-east-1.amazonaws.com",
                "s3BucketName": oplog_s3_bucket,
            }
        ]

        ops_manager.update()
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running,
            timeout=500,
            ignore_errors=True,
        )


@mark.e2e_om_ops_manager_backup_restore
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


@mark.e2e_om_ops_manager_backup_restore
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


@mark.e2e_om_ops_manager_backup_restore
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
