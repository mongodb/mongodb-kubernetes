from typing import Optional

from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongotester import MongoDBBackgroundTester
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests.conftest import LEGACY_OPERATOR_NAME
from tests.upgrades import downscale_operator_deployment


@fixture(scope="module")
def s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> str:
    """creates a s3 bucket and a s3 config"""

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


@fixture(scope="module")
def ops_manager(
    namespace: str, s3_bucket: str, custom_version: Optional[str], custom_appdb_version: str
) -> MongoDBOpsManager:
    """The fixture for Ops Manager to be created. Also results in a new s3 bucket
    created and used in OM spec"""
    om: MongoDBOpsManager = MongoDBOpsManager.from_yaml(yaml_fixture("om_ops_manager_full.yaml"), namespace=namespace)
    om.set_version(custom_version)
    om.set_appdb_version(custom_appdb_version)
    om["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket
    return om.create()


@fixture(scope="module")
def oplog_replica_set(ops_manager, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=ops_manager.namespace,
        name="my-mongodb-oplog",
    ).configure(ops_manager, "development")
    resource.set_version(custom_mdb_version)
    resource["spec"]["members"] = 1
    resource["spec"]["persistent"] = True

    yield resource.create()


@fixture(scope="module")
def s3_replica_set(ops_manager: MongoDBOpsManager, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=ops_manager.namespace,
        name="my-mongodb-s3",
    ).configure(ops_manager, "s3metadata")

    resource.set_version(custom_mdb_version)
    resource["spec"]["members"] = 1
    resource["spec"]["persistent"] = True

    yield resource.create()


@fixture(scope="module")
def some_mdb(ops_manager: MongoDBOpsManager, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=ops_manager.namespace,
        name="some-mdb",
    ).configure(ops_manager, "someProject")
    resource.set_version(custom_mdb_version)
    resource["spec"]["persistent"] = True

    return resource.create()


@fixture(scope="module")
def some_mdb_health_checker(some_mdb: MongoDB) -> MongoDBBackgroundTester:
    # TODO increasing allowed_sequential_failures to 5 to remove flakiness until CLOUDP-56877 is solved
    return MongoDBBackgroundTester(some_mdb.tester(), allowed_sequential_failures=5)


# The first stage of the Operator upgrade test. Create Ops Manager with backup enabled,
# creates backup databases and some extra database referencing the OM.
# TODO CLOUDP-54130: this database needs to get enabled for backup and this needs to be verified
# on the second stage


@mark.e2e_operator_upgrade_ops_manager
def test_install_latest_official_operator(official_operator: Operator):
    official_operator.assert_is_running()


@mark.e2e_operator_upgrade_ops_manager
def test_om_created(ops_manager: MongoDBOpsManager):
    ops_manager.backup_status().assert_reaches_phase(
        Phase.Pending,
        msg_regexp="The MongoDB object .+ doesn't exist",
        timeout=900,
    )


@mark.e2e_operator_upgrade_ops_manager
def test_backup_enabled(
    ops_manager: MongoDBOpsManager,
    oplog_replica_set: MongoDB,
    s3_replica_set: MongoDB,
):
    oplog_replica_set.assert_reaches_phase(Phase.Running)
    s3_replica_set.assert_reaches_phase(Phase.Running)
    # We are ignoring any errors as there could be temporary blips in connectivity to backing
    # databases by this time
    ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=200, ignore_errors=True)


@skip_if_local
@mark.e2e_operator_upgrade_ops_manager
def test_om_is_ok(ops_manager: MongoDBOpsManager):
    ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_operator_upgrade_ops_manager
def test_mdb_created(some_mdb: MongoDB):
    some_mdb.assert_reaches_phase(Phase.Running)
    # TODO we need to enable backup for the mongodb - it's critical to make sure the backup for
    # deployments continue to work correctly after upgrade


# This is a part 2 of the Operator upgrade test. Upgrades the Operator the latest development one and checks
# that everything works


@mark.e2e_operator_upgrade_ops_manager
def test_downscale_latest_official_operator(namespace: str):
    # Scale down the existing operator deployment to 0. This is needed as long as the
    # `official_operator` fixture installs the MEKO operator.
    downscale_operator_deployment(deployment_name=LEGACY_OPERATOR_NAME, namespace=namespace)


@mark.e2e_operator_upgrade_ops_manager
def test_upgrade_operator(default_operator: Operator):
    default_operator.assert_is_running()


@mark.e2e_operator_upgrade_ops_manager
def test_start_mongod_background_tester(
    some_mdb_health_checker: MongoDBBackgroundTester,
):
    some_mdb_health_checker.start()


@mark.e2e_operator_upgrade_ops_manager
def test_om_ok(ops_manager: MongoDBOpsManager):
    ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=1200)

    ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=400)
    ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=200)

    ops_manager.get_om_tester().assert_healthiness()


@mark.e2e_operator_upgrade_ops_manager
def test_some_mdb_ok(some_mdb: MongoDB, some_mdb_health_checker: MongoDBBackgroundTester):
    # TODO make sure the backup is working when it's implemented
    some_mdb.assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)
    # The mongodb was supposed to be healthy all the time
    some_mdb_health_checker.assert_healthiness()
