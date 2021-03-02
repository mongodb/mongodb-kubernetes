from typing import Optional

from kubetester import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import (
    skip_if_local,
    fixture as yaml_fixture,
)
from kubetester.mongodb import Phase
from pytest import mark, fixture
from tests.opsmanager.om_ops_manager_backup import (
    HEAD_PATH,
    OPLOG_RS_NAME,
    new_om_data_store,
    create_aws_secret,
    S3_SECRET_NAME,
    create_s3_bucket,
)

DEFAULT_APPDB_USER_NAME = "mongodb-ops-manager"

"""
This test checks the backup if no separate S3 Metadata database is created and AppDB is used for this.
Note, that it doesn't check for mongodb backup as it's done in 'e2e_om_ops_manager_backup_restore'" 
"""


@fixture(scope="module")
def s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> str:
    create_aws_secret(aws_s3_client, S3_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client)


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


@mark.e2e_om_ops_manager_backup_light
class TestOpsManagerCreation:
    def test_create_om(self, ops_manager: MongoDBOpsManager):
        """ creates a s3 bucket and an OM resource, the S3 configs get created using AppDB. Oplog store is still required. """
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            msg_regexp="Oplog Store configuration is required for backup",
            timeout=600,
        )

    def test_oplog_mdb_created(
        self,
        oplog_replica_set: MongoDB,
    ):
        oplog_replica_set.assert_reaches_phase(Phase.Running)

    def test_add_oplog_config(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["backup"]["opLogStores"] = [
            {"name": "oplog1", "mongodbResourceRef": {"name": "my-mongodb-oplog"}}
        ]
        ops_manager.update()

        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running,
            timeout=200,
            ignore_errors=True,
        )

    @skip_if_local
    def test_om(
        self,
        ops_manager: MongoDBOpsManager,
        s3_bucket: str,
        aws_s3_client: AwsS3Client,
        oplog_replica_set: MongoDB,
    ):
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        om_tester.assert_daemon_enabled(ops_manager.backup_daemon_pod_name(), HEAD_PATH)
        om_tester.assert_oplog_stores([new_om_data_store(oplog_replica_set, "oplog1")])

        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        ops_manager.assert_appdb_monitoring_group_was_created()

        # TODO uncomment when CLOUDP-70468 is fixed and AppDB supports scram-sha-256
        # making sure the s3 config pushed to OM references the appdb
        # appdb_replica_set = ops_manager.get_appdb_resource()
        # appdb_password = KubernetesTester.read_secret(
        #     ops_manager.namespace, ops_manager.app_db_password_secret_name()
        # )["password"]
        # om_tester.assert_s3_stores(
        #     [
        #         new_om_s3_store(
        #             appdb_replica_set,
        #             "s3Store1",
        #             s3_bucket,
        #             aws_s3_client,
        #             user_name=DEFAULT_APPDB_USER_NAME,
        #             password=appdb_password,
        #         )
        #     ]
        # )


@mark.e2e_om_ops_manager_backup_light
def test_backup_statefulset_remains_after_disabling_backup(
    ops_manager: MongoDBOpsManager,
):
    ops_manager.load()
    ops_manager["spec"]["backup"]["enabled"] = False
    ops_manager.update()

    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=200)

    # the backup statefulset should still exist even after we disable backup
    ops_manager.read_backup_statefulset()
