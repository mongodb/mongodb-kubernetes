from typing import Dict, Iterator, Optional

import semver
from kubetester import run_periodically, try_load
from kubetester.awss3client import AwsS3Client, s3_endpoint
from kubetester.kubetester import KubernetesTester
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark, skip
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
OBJECT_RETENTION_DAYS = 7
OBJECT_RETENTION_MODE = "GOVERNANCE"
# OM versions older than 8.0.27 reject the retention attributes (INVALID_ATTRIBUTE),
# so they must only be set on the CR when the OM version supports them.
OBJECT_RETENTION_MIN_OM_VERSION = "8.0.27"


def om_supports_object_retention(custom_version: Optional[str]) -> bool:
    # None means the test deploys the default OM version, which is always >= the
    # minimum version supporting the retention fields.
    if custom_version is None:
        return True
    return semver.VersionInfo.parse(custom_version) >= semver.VersionInfo.parse(OBJECT_RETENTION_MIN_OM_VERSION)


def feature_detect_retention(expected_store: Dict, actual_store: Dict) -> Dict:
    """OM returns the retention fields only on versions exposing the S3 object-lock
    retention API (8.0.27+). Adds the retention expectations only when the API actually
    returns them, so the store comparison works on any OM version."""
    if "objectRetentionMode" in actual_store:
        expected_store["objectRetentionDays"] = OBJECT_RETENTION_DAYS
        expected_store["objectRetentionMode"] = OBJECT_RETENTION_MODE
    return expected_store


"""
Current test focuses on backup capabilities. It creates an explicit MDBs for S3 snapshot metadata, Blockstore and Oplog
databases. Tests backup enabled for both MDB 4.0 and 4.2, snapshots created
"""


def new_om_s3_store(
    mdb: MongoDB,
    s3_id: str,
    s3_bucket_name: str,
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
def aws_secret(aws_s3_client: AwsS3Client, namespace: str) -> None:
    create_aws_secret(aws_s3_client, S3_SECRET_NAME, namespace)


@fixture(scope="module")
def s3_bucket_lock_only(aws_secret, aws_s3_client: AwsS3Client) -> Iterator[str]:
    # This bucket never carries a default retention rule: its snapshot store exercises
    # immutable backups with object lock alone.
    yield from create_s3_bucket(aws_s3_client, "test-bucket-s3-lock")


@fixture(scope="module")
def s3_bucket_retention(aws_secret, aws_s3_client: AwsS3Client) -> Iterator[str]:
    # Paired with a store that also sets the retention fields (when the OM version
    # supports them): OM's store validation requires the bucket and the store to agree.
    yield from create_s3_bucket(aws_s3_client, "test-bucket-s3-retention")


@fixture(scope="module")
def s3_buckets(s3_bucket_lock_only: str, s3_bucket_retention: str) -> Dict[str, str]:
    return {"lock": s3_bucket_lock_only, "retention": s3_bucket_retention}


@fixture(scope="module")
def ops_manager(
    namespace: str,
    s3_buckets: Dict[str, str],
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

    # Two snapshot stores, both object-lock-enabled: s3Store1 on a bucket that never
    # carries a retention rule (immutable backups with object lock alone), s3Store2 on
    # the retention bucket. Neither store sets the retention fields yet: the tests
    # assert the bucket/store mismatch failure first, then fix s3Store2 by adding them.
    resource["spec"]["backup"]["s3Stores"] = [
        {
            "name": "s3Store1",
            "s3SecretRef": {"name": S3_SECRET_NAME},
            "pathStyleAccessEnabled": True,
            "s3BucketEndpoint": s3_endpoint(AWS_REGION),
            "s3BucketName": s3_buckets["lock"],
            "objectLockEnabled": True,
        },
        {
            "name": "s3Store2",
            "s3SecretRef": {"name": S3_SECRET_NAME},
            "pathStyleAccessEnabled": True,
            "s3BucketEndpoint": s3_endpoint(AWS_REGION),
            "s3BucketName": s3_buckets["retention"],
            "objectLockEnabled": True,
        },
    ]

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

    def test_enable_versioning(self, s3_buckets: Dict[str, str], aws_s3_client: AwsS3Client):
        for bucket in s3_buckets.values():
            aws_s3_client.enable_versioning(bucket)

        def versioning_is_enabled():
            try:
                return all(
                    aws_s3_client.get_versioning(bucket)["Status"] == "Enabled" for bucket in s3_buckets.values()
                )
            except Exception:
                return False

        # We need to ensure that versioning is set before enabling object lock
        run_periodically(versioning_is_enabled, timeout=300)

    def test_enable_object_lock(
        self, s3_buckets: Dict[str, str], aws_s3_client: AwsS3Client, custom_version: Optional[str]
    ):
        # The bucket and the OM store must agree on retention: OM's store validation
        # rejects a retention-configured bucket paired with a store without retention
        # settings (and vice versa). The lock bucket therefore never carries a default
        # retention rule; the retention bucket only gets one when the OM version supports
        # the retention fields — otherwise the mismatch could never be fixed.
        aws_s3_client.put_object_lock(s3_buckets["lock"])

        retention_supported = om_supports_object_retention(custom_version)
        if retention_supported:
            aws_s3_client.put_object_lock(
                s3_buckets["retention"], retention_days=OBJECT_RETENTION_DAYS, retention_mode=OBJECT_RETENTION_MODE
            )
        else:
            aws_s3_client.put_object_lock(s3_buckets["retention"])

        expected_buckets = {
            s3_buckets["lock"]: None,
            s3_buckets["retention"]: OBJECT_RETENTION_DAYS if retention_supported else None,
        }
        for bucket, expected_days in expected_buckets.items():
            lock_config = aws_s3_client.get_object_lock(bucket)["ObjectLockConfiguration"]
            assert lock_config["ObjectLockEnabled"] == "Enabled"
            if expected_days is None:
                assert "Rule" not in lock_config
            else:
                assert lock_config["Rule"]["DefaultRetention"] == {
                    "Mode": OBJECT_RETENTION_MODE,
                    "Days": expected_days,
                }

    def test_om_fails_with_bucket_retention_mismatch(
        self, ops_manager: MongoDBOpsManager, custom_version: Optional[str]
    ):
        # The retention bucket has a default retention rule, but s3Store2 does not set
        # the retention fields: OM must reject this mismatch.
        if not om_supports_object_retention(custom_version):
            skip("bucket retention mismatch requires OM versions exposing the retention API (8.0.27+)")
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Failed,
            # msg_regexp is matched with re.match (anchored at the message start); (?s)
            # lets the match span the newlines inside the validation detail.
            msg_regexp="(?s)^Status:.*validateObjectRetentionEnabled",
            timeout=600,
        )

    def test_om_fixed_by_adding_retention(self, ops_manager: MongoDBOpsManager, custom_version: Optional[str]):
        # Adding the retention fields to s3Store2 makes the store agree with its bucket,
        # so the backup must recover to Running.
        if not om_supports_object_retention(custom_version):
            skip("bucket retention mismatch requires OM versions exposing the retention API (8.0.27+)")
        ops_manager["spec"]["backup"]["s3Stores"][1]["objectRetentionDays"] = OBJECT_RETENTION_DAYS
        ops_manager["spec"]["backup"]["s3Stores"][1]["objectRetentionMode"] = OBJECT_RETENTION_MODE
        ops_manager.update()

    def test_om_passes_validations(self, ops_manager: MongoDBOpsManager):
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running,
            timeout=600,
        )

    def test_om_object_lock_enabled(self, ops_manager: MongoDBOpsManager, s3_buckets: Dict[str, str]):
        om_tester = ops_manager.get_om_tester()
        appdb_replica_set = ops_manager.get_appdb_resource()
        appdb_password = KubernetesTester.read_secret(ops_manager.namespace, ops_manager.app_db_password_secret_name())[
            "password"
        ]
        lock_only_store = new_om_s3_store(
            appdb_replica_set,
            "s3Store1",
            s3_buckets["lock"],
            user_name=DEFAULT_APPDB_USER_NAME,
            password=appdb_password,
            object_lock_enabled=True,
        )
        retention_store = new_om_s3_store(
            appdb_replica_set,
            "s3Store2",
            s3_buckets["retention"],
            user_name=DEFAULT_APPDB_USER_NAME,
            password=appdb_password,
            object_lock_enabled=True,
        )

        # OM returns the retention fields only on versions exposing the S3 object-lock
        # retention API (8.0.27+). Only the second store sets them on such versions; keep
        # the assertion feature-detected so the store comparison works on any variant.
        actual_stores = {store["id"]: store for store in om_tester.get_s3_stores()["results"]}
        expected_stores = [
            lock_only_store,
            feature_detect_retention(retention_store, actual_stores["s3Store2"]),
        ]

        om_tester.assert_s3_stores(expected_stores)
