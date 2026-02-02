from typing import Dict, Optional

from kubetester import run_periodically, try_load
from kubetester.awss3client import AwsS3Client, s3_endpoint
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.constants import AWS_REGION
from tests.opsmanager.om_ops_manager_backup import create_aws_secret, create_s3_bucket
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

HEAD_PATH = "/head/"
S3_SECRET_NAME = "my-s3-secret"
OPLOG_RS_NAME = "my-mongodb-oplog"
S3_RS_NAME = "my-mongodb-s3"
BLOCKSTORE_RS_NAME = "my-mongodb-blockstore"
USER_PASSWORD = "/qwerty@!#:"
DEFAULT_APPDB_USER_NAME = "mongodb-ops-manager"

"""
Current test focuses on backup capabilities. It creates an explicit MDBs for S3 snapshot metadata, Blockstore and Oplog
databases. Tests backup enabled for both MDB 4.0 and 4.2, snapshots created
"""


def new_om_s3_store(
    mdb: MongoDB,
    s3_id: str,
    s3_bucket_name: str,
    aws_s3_client: AwsS3Client,
    assignment_enabled: bool = True,
    path_style_access_enabled: bool = True,
    user_name: Optional[str] = None,
    password: Optional[str] = None,
    object_lock_enabled: bool = False,
) -> Dict:
    return {
        "uri": mdb.mongo_uri(user_name=user_name, password=password),
        "id": s3_id,
        "pathStyleAccessEnabled": path_style_access_enabled,
        "s3BucketEndpoint": s3_endpoint(AWS_REGION),
        "s3BucketName": s3_bucket_name,
        "awsAccessKey": aws_s3_client.aws_access_key,
        "awsSecretKey": aws_s3_client.aws_secret_access_key,
        "assignmentEnabled": assignment_enabled,
        "objectLockEnabled": object_lock_enabled,
    }


@fixture(scope="module")
def oplog_replica_set(ops_manager, namespace, custom_mdb_version) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=OPLOG_RS_NAME,
    ).configure(ops_manager, "development")

    resource.set_version(custom_mdb_version)

    try_load(resource)

    return resource


@fixture(scope="module")
def s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> str:
    create_aws_secret(aws_s3_client, S3_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, "test-bucket-s3")


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

    if try_load(resource):
        return resource

    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource["spec"]["backup"]["members"] = 1

    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket
    resource["spec"]["backup"]["s3Stores"][0]["objectLock"] = True

    resource["spec"]["configuration"]["brs.immutableBackupEnabled"] = "true"

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    resource.update()
    return resource


@mark.e2e_om_ops_manager_backup_object_lock
class TestOpsManagerCreation:
    def test_create_om(self, ops_manager: MongoDBOpsManager):
        """creates a s3 bucket and an OM resource, the S3 configs get created using AppDB. Oplog store is still required."""
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)

    def test_oplog_mdb_created(
        self,
        oplog_replica_set: MongoDB,
    ):
        oplog_replica_set.update()
        oplog_replica_set.assert_reaches_phase(Phase.Running)

    def test_add_oplog_config(self, ops_manager: MongoDBOpsManager):
        # Keeping this assertion here speeds up the test by deploying the oplog MDB earlier
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            msg_regexp="Oplog Store configuration is required for backup",
            timeout=600,
        )

        ops_manager["spec"]["backup"]["opLogStores"] = [
            {"name": "oplog1", "mongodbResourceRef": {"name": "my-mongodb-oplog"}}
        ]
        ops_manager.update()

    def test_s3_bucket_validation_fails(self, ops_manager: MongoDBOpsManager):
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Failed,
            timeout=500,
        )

    def test_enable_versioning_and_object_lock_on_bucket(self, s3_bucket: str, aws_s3_client: AwsS3Client):
        aws_s3_client.enable_versioning(s3_bucket)

        def versioning_is_enabled():
            return aws_s3_client.get_versioning(s3_bucket)["Status"] == "Enabled"

        # We need to ensure that versioning is set before enabling object lock
        run_periodically(versioning_is_enabled, timeout=300)

    def test_enable_object_lock(self, s3_bucket: str, aws_s3_client: AwsS3Client):
        aws_s3_client.put_object_lock(s3_bucket)

    def test_om_passes_validations(self, ops_manager: MongoDBOpsManager):
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running,
            timeout=600,
        )

    def test_om_object_lock_enabled(self, ops_manager: MongoDBOpsManager, s3_bucket: str, aws_s3_client: AwsS3Client):
        om_tester = ops_manager.get_om_tester()
        appdb_replica_set = ops_manager.get_appdb_resource()
        appdb_password = KubernetesTester.read_secret(ops_manager.namespace, ops_manager.app_db_password_secret_name())[
            "password"
        ]
        om_tester.assert_s3_stores(
            [
                new_om_s3_store(
                    appdb_replica_set,
                    "s3Store1",
                    s3_bucket,
                    aws_s3_client,
                    user_name=DEFAULT_APPDB_USER_NAME,
                    password=appdb_password,
                    object_lock_enabled=True,
                )
            ]
        )
