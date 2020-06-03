import datetime
from os import environ

import time
from kubetester import MongoDB
from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import Phase
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from pytest import mark, fixture
from tests.opsmanager.om_ops_manager_backup import (
    OPLOG_RS_NAME,
    create_aws_secret,
    S3_SECRET_NAME,
    create_s3_bucket,
)

"""
The test checks the backup for MongoDB 4.0 and 4.2, checks that snapshots are built and PIT restore and 
restore from snapshot are working.
"""

TEST_DATA = {"name": "John", "address": "Highway 37", "age": 30}


@fixture(scope="module")
def s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> str:
    create_aws_secret(aws_s3_client, S3_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client)


@fixture(scope="module")
def ops_manager(namespace, s3_bucket) -> MongoDBOpsManager:
    # TODO we need to use 4.2.13 OM in order to check PIT restore - so far the test is run in OM 4.4+ only
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup_light.yaml"), namespace=namespace
    )
    if "CUSTOM_OM_VERSION" in environ:
        resource["spec"]["version"] = environ.get("CUSTOM_OM_VERSION")
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket

    return resource.create()


@fixture(scope="module")
def oplog_replica_set(ops_manager, namespace) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=OPLOG_RS_NAME,
    ).configure(ops_manager, "development")

    return resource.create()


@fixture(scope="module")
def mdb_4_2(ops_manager: MongoDBOpsManager, namespace):
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name="mdb-four-two",
    ).configure(ops_manager, "fourTwoProject")
    # MongoD versions greater than 4.2.0 must be enterprise build to enable backup
    resource["spec"]["version"] = "4.2.6-ent"

    return resource.create()


@fixture(scope="module")
def mdb_4_0(ops_manager: MongoDBOpsManager, namespace):
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name="mdb-four-zero",
    ).configure(ops_manager, "fourZeroProject")
    resource["spec"]["version"] = "4.0.18"

    return resource.create()


@fixture(scope="module")
def mdb_4_0_test_collection(mdb_4_0):
    db_4_0 = mdb_4_0.tester().client["testdb"]
    return db_4_0["testcollection"]


@fixture(scope="module")
def mdb_4_2_test_collection(mdb_4_2):
    db_4_2 = mdb_4_2.tester().client["testdb"]
    return db_4_2["testcollection"]


@fixture(scope="module")
def mdb_4_0_project(ops_manager: MongoDBOpsManager) -> OMTester:
    return ops_manager.get_om_tester(project_name="fourZeroProject")


@fixture(scope="module")
def mdb_4_2_project(ops_manager: MongoDBOpsManager) -> OMTester:
    return ops_manager.get_om_tester(project_name="fourTwoProject")


@mark.e2e_om_ops_manager_backup_restore
class TestOpsManagerCreation:
    def test_create_om(self, ops_manager: MongoDBOpsManager):
        """ creates a s3 bucket and an OM resource, the S3 configs get created using AppDB. Oplog store is still required. """
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            msg_regexp="Oplog Store configuration is required for backup",
            timeout=300,
        )

    def test_oplog_mdb_created(
        self, oplog_replica_set: MongoDB,
    ):
        oplog_replica_set.assert_reaches_phase(Phase.Running)

    def test_add_oplog_config(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["backup"]["oplogStores"] = [
            {"name": "oplog1", "mongodbResourceRef": {"name": "my-mongodb-oplog"}}
        ]
        ops_manager.update()
        ops_manager.om_status().assert_abandons_phase(Phase.Running)
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running, timeout=200, ignore_errors=True,
        )


@mark.e2e_om_ops_manager_backup_restore
class TestBackupForMongodb:
    """ This part ensures that backup for the client works correctly and the snapshot is created.
    Both Mdb 4.0 and 4.2 are tested (as the backup process for them differs significantly) """

    def test_mdbs_created(self, mdb_4_2: MongoDB, mdb_4_0: MongoDB):
        mdb_4_2.assert_reaches_phase(Phase.Running)
        mdb_4_0.assert_reaches_phase(Phase.Running)

    def test_add_test_data(self, mdb_4_0_test_collection, mdb_4_2_test_collection):
        mdb_4_0_test_collection.insert_one(TEST_DATA)
        mdb_4_2_test_collection.insert_one(TEST_DATA)

    def test_mdbs_backed_up(self, mdb_4_0_project: OMTester, mdb_4_2_project: OMTester):
        mdb_4_0_project.enable_backup()
        mdb_4_2_project.enable_backup()

        # wait until a first snapshot is ready for both
        mdb_4_0_project.wait_until_backup_snapshots_are_ready(expected_count=1)
        mdb_4_2_project.wait_until_backup_snapshots_are_ready(expected_count=1)


@mark.e2e_om_ops_manager_backup_restore
class TestBackupRestorePIT:
    """ This part checks the work of PIT restore. """

    def test_mdbs_change_data(self, mdb_4_0_test_collection, mdb_4_2_test_collection):
        """ Changes the MDB documents to check that restore rollbacks this change later.
         Note, that we need to wait for some time to ensure the PIT timestamp gets to the range
         [snapshot_created <= PIT <= changes_applied] """
        now_millis = time_to_millis(datetime.datetime.now())
        print("\nCurrent time (millis): {}".format(now_millis))
        time.sleep(30)

        mdb_4_0_test_collection.insert_one({"foo": "bar"})
        mdb_4_2_test_collection.insert_one({"foo": "bar"})

    def test_mdbs_pit_restore(
        self, mdb_4_0_project: OMTester, mdb_4_2_project: OMTester
    ):
        now_millis = time_to_millis(datetime.datetime.now())
        print("\nCurrent time (millis): {}".format(now_millis))

        pit_datetme = datetime.datetime.now() - datetime.timedelta(seconds=15)
        pit_millis = time_to_millis(pit_datetme)
        print(
            "Restoring back to the moment 15 seconds ago (millis): {}".format(
                pit_millis
            )
        )

        mdb_4_0_project.create_restore_job_pit(pit_millis)
        mdb_4_2_project.create_restore_job_pit(pit_millis)

        # Note, that we are not waiting for the restore jobs to get finished as PIT restore jobs get FINISHED status
        # right away

    def test_data_got_restored(self, mdb_4_0_test_collection, mdb_4_2_test_collection):
        """ The data in the db has been restored to the initial state. Note, that this happens eventually - so
        we need to loop for some time (usually takes 20 seconds max). This is different from restoring from a
        specific snapshot (see the previous class) where the FINISHED restore job means the data has been restored.
        For PIT restores FINISHED just means the job has been created and the agents will perform restore eventually """
        print("\nWaiting until the db data is restored")
        retries = 120
        while retries > 0:
            try:
                records = list(mdb_4_0_test_collection.find())
                assert records == [TEST_DATA]

                records = list(mdb_4_2_test_collection.find())
                assert records == [TEST_DATA]
                return
            except AssertionError:
                pass
            except Exception as e:
                # We ignore Exception as there is usually a blip in connection (backup restore
                # results in reelection or whatever)
                # "Connection reset by peer" or "not master and slaveOk=false"
                print("Exception happened while waiting for db data restore: ", e)
                # this is definitely the sign of a problem - no need continuing as each connection times out
                # after many minutes
                if "Connection refused" in str(e):
                    raise e
            retries -= 1
            time.sleep(1)

        print("\nExisting data in 4.0: {}".format(list(mdb_4_0_test_collection.find())))
        print("Existing data in 4.2: {}".format(list(mdb_4_2_test_collection.find())))

        raise AssertionError("The data hasn't been restored in 2 minutes!")


@mark.e2e_om_ops_manager_backup_restore
class TestBackupRestoreFromSnapshot:
    """ This part tests the restore to the snapshot built once the backup has been enabled. """

    def test_mdbs_change_data(self, mdb_4_0_test_collection, mdb_4_2_test_collection):
        """ Changes the MDB documents to check that restore rollbacks this change later """
        mdb_4_0_test_collection.delete_many({})
        mdb_4_0_test_collection.insert_one({"foo": "bar"})

        mdb_4_2_test_collection.delete_many({})
        mdb_4_2_test_collection.insert_one({"foo": "bar"})

    def test_mdbs_automated_restore(
        self, mdb_4_0_project: OMTester, mdb_4_2_project: OMTester
    ):
        restore_4_0_id = mdb_4_0_project.create_restore_job_snapshot()
        restore_4_2_id = mdb_4_2_project.create_restore_job_snapshot()

        mdb_4_0_project.wait_until_restore_job_is_ready(restore_4_0_id)
        mdb_4_2_project.wait_until_restore_job_is_ready(restore_4_2_id)

    def test_data_got_restored(self, mdb_4_0_test_collection, mdb_4_2_test_collection):
        """ The data in the db has been restored to the initial"""
        records = list(mdb_4_0_test_collection.find())
        assert records == [TEST_DATA]

        records = list(mdb_4_2_test_collection.find())
        assert records == [TEST_DATA]


def time_to_millis(date_time) -> int:
    """ https://stackoverflow.com/a/11111177/614239"""
    epoch = datetime.datetime.utcfromtimestamp(0)
    pit_millis = (date_time - epoch).total_seconds() * 1000
    return pit_millis
