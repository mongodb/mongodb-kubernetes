"""
The second stage of an Operator-upgrade test.
This ensures the OM is functional and a mongodb instance referencing OM is working
"""
from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, MongoDBOpsManager, Phase
from kubetester.mongotester import MongoDBBackgroundTester
from pytest import fixture, mark


@fixture(scope="module")
def ops_manager(namespace: str) -> MongoDBOpsManager:
    """ The fixture for Ops Manager to be loaded (not created!) """
    return MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_full.yaml"), namespace=namespace
    ).load()


@fixture(scope="module")
def s3_bucket(
    aws_s3_client: AwsS3Client, namespace, ops_manager: MongoDBOpsManager
) -> str:
    """ recreates the s3 bucket which was removed by e2e_op_upgrade_om_first test.
     Note, that the secret already exists. """

    bucket_name = ops_manager["spec"]["backup"]["s3Stores"][0]["s3BucketName"]
    aws_s3_client.create_s3_bucket(bucket_name)
    print(f"\nCreated S3 bucket {bucket_name} which was removed after the first stage")

    yield bucket_name

    print(f"\nRemoving S3 bucket {bucket_name}")
    aws_s3_client.delete_s3_bucket(bucket_name)


@fixture(scope="module")
def some_mdb(ops_manager, namespace) -> MongoDB:
    return MongoDB(namespace=namespace, name="some-mdb",).load()


@fixture(scope="module")
def some_mdb_health_checker(some_mdb) -> MongoDBBackgroundTester:
    return MongoDBBackgroundTester(some_mdb._tester())


@mark.e2e_op_upgrade_om_second
class TestOpsManagerWorksOkAfterOperatorUpgrade:
    """ This is a part 2 of the Operator upgrade test. Note, that it's launched in a new test pod
     so doesn't share the state with the TestOpsManagerInstalledFirst class"""

    def test_start_mongod_background_tester(
        self, some_mdb_health_checker: MongoDBBackgroundTester
    ):
        some_mdb_health_checker.start()

    def test_s3_bucket_recreated(self, s3_bucket: str):
        """ Implicit creation of the s3 bucket"""
        pass

    def test_om_ok(self, ops_manager: MongoDBOpsManager):
        ops_manager.assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.get_om_tester().assert_healthiness()

        current_om_image = (
            ops_manager.get_statefulset().spec.template.spec.containers[0].image
        )
        assert (
            current_om_image != ops_manager["metadata"]["annotations"]["last_om_image"]
        )
        print(
            "The reconciliation happened on Operator upgrade and the OM image was updated"
        )

    def test_some_mdb_ok(
        self, some_mdb: MongoDB, some_mdb_health_checker: MongoDBBackgroundTester
    ):
        # TODO make sure the backup is working when it's implemented
        some_mdb.assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)
        # The mongodb was supposed to be healthy all the time
        some_mdb_health_checker.assert_healthiness()
