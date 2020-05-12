"""
The fist stage of an Operator-upgrade test.
It creates an OM instance with maximum features (backup, scram etc).
Also it creates a MongoDB referencing the OM.
"""
from os import environ

from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import (
    skip_if_local,
    fixture as yaml_fixture,
    KubernetesTester,
)
from kubetester.mongodb import MongoDB, Phase
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark


@fixture(scope="module")
def s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> str:
    """ creates a s3 bucket and a s3 config"""

    bucket_name = KubernetesTester.random_k8s_name("test-bucket-")
    aws_s3_client.create_s3_bucket(bucket_name)
    print(f"\nCreated S3 bucket {bucket_name}")

    KubernetesTester.create_secret(
        namespace,
        "my-s3-secret",
        {
            "accessKey": aws_s3_client.aws_access_key,
            "secretKey": aws_s3_client.aws_secret_access_key,
        },
    )
    yield bucket_name

    print(f"\nRemoving S3 bucket {bucket_name}")
    aws_s3_client.delete_s3_bucket(bucket_name)


@fixture(scope="module")
def ops_manager(namespace: str, s3_bucket) -> MongoDBOpsManager:
    """ The fixture for Ops Manager to be created. Also results in a new s3 bucket
    created and used in OM spec"""
    om = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_full.yaml"), namespace=namespace
    )
    if "CUSTOM_OM_VERSION" in environ:
        om["spec"]["version"] = environ.get("CUSTOM_OM_VERSION")
    om["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket
    return om.create()


@fixture(scope="module")
def oplog_replica_set(ops_manager, namespace):
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name="my-mongodb-oplog",
    ).configure(ops_manager, "development")
    resource["spec"]["members"] = 1
    resource["spec"]["persistent"] = True

    yield resource.create()


@fixture(scope="module")
def s3_replica_set(ops_manager, namespace):
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name="my-mongodb-s3",
    ).configure(ops_manager, "s3metadata")
    resource["spec"]["members"] = 1
    resource["spec"]["persistent"] = True

    yield resource.create()


@fixture(scope="module")
def some_mdb(ops_manager, namespace):
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"), namespace=namespace, name="some-mdb",
    ).configure(ops_manager, "someProject")
    resource["spec"]["persistent"] = True

    return resource.create()


@mark.e2e_op_upgrade_om_first
class TestOpsManagerInstalledFirst:
    """ The first stage of the Operator upgrade test. Create Ops Manager with backup enabled,
    creates backup databases and some extra database referencing the OM.
    TODO CLOUDP-54130: this database needs to get enabled for backup and this needs to be verified
    on the second stage"""

    def test_om_created(self, ops_manager: MongoDBOpsManager):
        try:
            ops_manager.backup_status().assert_reaches_phase(
                Phase.Pending,
                msg_regexp="The MongoDB object .+ doesn't exist",
                timeout=900,
            )
        except:
            # Operator versions <= 1.4.4 didn't have "backup" status - so the message about
            # non-existent backup dbs got into "status.opsManager"
            assert "backup" not in ops_manager.get_status()
            ops_manager.om_status().assert_reaches_phase(
                Phase.Pending,
                msg_regexp="The MongoDB object .+ doesn't exist",
                timeout=60,
            )

    def test_backup_enabled(
        self,
        ops_manager: MongoDBOpsManager,
        oplog_replica_set: MongoDB,
        s3_replica_set: MongoDB,
    ):
        oplog_replica_set.assert_reaches_phase(Phase.Running)
        s3_replica_set.assert_reaches_phase(Phase.Running)
        # We are ignoring any errors as there could be temporary blips in connectivity to backing
        # databases by this time
        try:
            ops_manager.backup_status().assert_reaches_phase(
                Phase.Running, timeout=200, ignore_errors=True
            )
        except:
            # failback logic for older versions
            assert "backup" not in ops_manager.get_status()
            assert ops_manager.om_status().get_phase() == Phase.Running

    @skip_if_local
    def test_om_is_ok(self, ops_manager: MongoDBOpsManager):
        ops_manager.get_om_tester().assert_healthiness()

    def test_mdb_created(self, some_mdb: MongoDB):
        some_mdb.assert_reaches_phase(Phase.Running)
        # TODO we need to enable backup for the mongodb - it's critical to make sure the backup for
        # deployments continue to work correctly after upgrade
