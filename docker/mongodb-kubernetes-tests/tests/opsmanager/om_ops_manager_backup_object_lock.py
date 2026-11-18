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
from tests.conftest import get_custom_om_version, is_multi_cluster
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


# OM supports object lock on the OpLog S3 config endpoint only from 8.0.28 (snapshot
# stores support it since 8.0.19 / retention since 8.0.27). Older versions reject
# objectLockEnabled there with INVALID_ATTRIBUTE, and the operator validation rejects
# the CR as well.
OPLOG_OBJECT_LOCK_MIN_OM_VERSION = "8.0.28"


def om_supports_oplog_object_lock(custom_version: Optional[str]) -> bool:
    # None means no custom OM version was pinned; the Evergreen variants running this
    # test always deploy a recent OM, so treat it as supported.
    if custom_version is None:
        return True
    return semver.VersionInfo.parse(custom_version) >= semver.VersionInfo.parse(OPLOG_OBJECT_LOCK_MIN_OM_VERSION)


# Not a plain `>= 8.0.28`: 9.0.0 and 9.0.1 don't have the change.
OBJECT_LOCK_INPUT_IGNORED_8_0_MIN_OM_VERSION = "8.0.28"
OBJECT_LOCK_INPUT_IGNORED_9_0_MIN_OM_VERSION = "9.0.2"


def om_ignores_object_lock_input(custom_version: Optional[str]) -> bool:
    """Whether this OM version reads the object-lock settings from the bucket and discards
    them from the store input (CLOUDP-470804). The operator no longer sends them to such
    versions and supports portableBackupEnabled instead (KUBE-632)."""
    if custom_version is None:
        return False
    v = semver.VersionInfo.parse(custom_version)
    is_8_0_with_change = (
        semver.VersionInfo.parse(OBJECT_LOCK_INPUT_IGNORED_8_0_MIN_OM_VERSION) <= v < semver.VersionInfo.parse("9.0.0")
    )
    is_9_0_with_change = v >= semver.VersionInfo.parse(OBJECT_LOCK_INPUT_IGNORED_9_0_MIN_OM_VERSION)
    return is_8_0_with_change or is_9_0_with_change


# Evaluated at collection time to pick the version-specific test classes below; the
# custom_version fixture reads the same value.
BUCKET_DERIVED_OBJECT_LOCK = om_ignores_object_lock_input(get_custom_om_version())


def feature_detect_retention(expected_store: Dict, actual_store: Dict) -> Dict:
    """OM returns the retention fields only on versions exposing the S3 object-lock
    retention API (8.0.27+ for snapshot stores, 8.0.28+ for oplog stores). Adds the
    retention expectations only when the API actually returns them, so the store
    comparison works on any OM version."""
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


def new_om_appdb_s3_store(ops_manager: MongoDBOpsManager, s3_id: str, s3_bucket_name: str, **fields) -> Dict:
    """Expected OM view of an S3 store backed by the AppDB."""
    appdb_password = KubernetesTester.read_secret(ops_manager.namespace, ops_manager.app_db_password_secret_name())[
        "password"
    ]
    store = new_om_s3_store(
        ops_manager.get_appdb_resource(),
        s3_id,
        s3_bucket_name,
        user_name=DEFAULT_APPDB_USER_NAME,
        password=appdb_password,
    )
    store.update(fields)
    return store


def s3_store_spec(name: str, bucket: str) -> Dict:
    return {
        "name": name,
        "s3SecretRef": {"name": S3_SECRET_NAME},
        "pathStyleAccessEnabled": True,  # required by the CRD schema
        "s3BucketEndpoint": s3_endpoint(AWS_REGION),
        "s3BucketName": bucket,
    }


def deprecation_warnings(ops_manager: MongoDBOpsManager) -> list[str]:
    return [w for w in ops_manager.backup_status().get_warnings() if "deprecated and ignored" in w]


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
def s3_bucket_oplog(aws_secret, aws_s3_client: AwsS3Client) -> Iterator[str]:
    # Dedicated bucket for the S3 oplog store: OM's store validation requires the
    # bucket default retention rule and the store settings to agree, so the oplog
    # store must not share the snapshot stores' buckets.
    yield from create_s3_bucket(aws_s3_client, "test-bucket-s3-oplog")


@fixture(scope="module")
def s3_buckets(s3_bucket_lock_only: str, s3_bucket_retention: str, s3_bucket_oplog: str) -> Dict[str, str]:
    return {"lock": s3_bucket_lock_only, "retention": s3_bucket_retention, "oplog": s3_bucket_oplog}


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

    # Two snapshot stores: s3Store1 on a bucket locked without a default retention rule,
    # s3Store2 on a bucket with one.
    lock_only_store = s3_store_spec("s3Store1", s3_buckets["lock"])
    retention_store = s3_store_spec("s3Store2", s3_buckets["retention"])
    if BUCKET_DERIVED_OBJECT_LOCK:
        # s3Store2 uses the recommended configuration. s3Store1 keeps the deprecated
        # field so the deprecation warning can be asserted.
        lock_only_store["objectLockEnabled"] = True
        lock_only_store["portableBackupEnabled"] = True
        retention_store["portableBackupEnabled"] = True
    else:
        # Neither store sets the retention fields yet: the legacy tests assert the
        # bucket/store mismatch failure first, then fix s3Store2 by adding them.
        lock_only_store["objectLockEnabled"] = True
        retention_store["objectLockEnabled"] = True
    resource["spec"]["backup"]["s3Stores"] = [lock_only_store, retention_store]

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

    def test_add_oplog_config(
        self, ops_manager: MongoDBOpsManager, s3_buckets: Dict[str, str], custom_version: Optional[str]
    ):
        # Keeping this assertion here speeds up the test by deploying the oplog MDB earlier
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            msg_regexp="Oplog Store configuration is required for backup",
            timeout=600,
        )

        ops_manager["spec"]["backup"]["opLogStores"] = [
            {"name": "oplog1", "mongodbResourceRef": {"name": "my-mongodb-oplog"}}
        ]
        s3_oplog_store = s3_store_spec("oplogS3Store1", s3_buckets["oplog"])
        if BUCKET_DERIVED_OBJECT_LOCK:
            s3_oplog_store["portableBackupEnabled"] = True
        elif om_supports_oplog_object_lock(custom_version):
            # OM supports object lock on oplog stores only from 8.0.28. On older versions
            # the store omits objectLockEnabled entirely (the operator validation rejects
            # a non-nil value there) and its bucket stays unlocked so store and bucket
            # agree. When supported, the store sets the retention fields, matching the
            # default retention rule test_enable_object_lock puts on its bucket.
            s3_oplog_store["objectLockEnabled"] = True
            s3_oplog_store["objectRetentionDays"] = OBJECT_RETENTION_DAYS
            s3_oplog_store["objectRetentionMode"] = OBJECT_RETENTION_MODE
        ops_manager["spec"]["backup"]["s3OpLogStores"] = [s3_oplog_store]
        ops_manager.update()

    def test_s3_bucket_validation_fails(self, ops_manager: MongoDBOpsManager):
        # On bucket-derived versions OM reads the Object Lock settings from the bucket when
        # it creates a store and rejects portableBackupEnabled while the bucket is unlocked.
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Failed,
            msg_regexp="(?s).*portable backup requires object lock" if BUCKET_DERIVED_OBJECT_LOCK else None,
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
        # Legacy OM requires the bucket and store retention settings to agree. Bucket-derived
        # versions read these settings directly from the bucket instead.
        aws_s3_client.put_object_lock(s3_buckets["lock"])

        retention_supported = om_supports_object_retention(custom_version)
        if retention_supported:
            aws_s3_client.put_object_lock(
                s3_buckets["retention"], retention_days=OBJECT_RETENTION_DAYS, retention_mode=OBJECT_RETENTION_MODE
            )
        else:
            aws_s3_client.put_object_lock(s3_buckets["retention"])

        # OpLog stores support Object Lock from 8.0.28. Older versions keep this bucket
        # unlocked; bucket-derived versions read the enabled bucket settings directly.
        oplog_lock_supported = om_supports_oplog_object_lock(custom_version)
        if oplog_lock_supported:
            aws_s3_client.put_object_lock(
                s3_buckets["oplog"], retention_days=OBJECT_RETENTION_DAYS, retention_mode=OBJECT_RETENTION_MODE
            )

        expected_buckets = {
            s3_buckets["lock"]: None,
            s3_buckets["retention"]: OBJECT_RETENTION_DAYS if retention_supported else None,
        }
        if oplog_lock_supported:
            expected_buckets[s3_buckets["oplog"]] = OBJECT_RETENTION_DAYS
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


@mark.e2e_om_ops_manager_backup_object_lock
@mark.skipif(
    BUCKET_DERIVED_OBJECT_LOCK,
    reason="Ops Manager 8.0.28+/9.0.2+ reads the Object Lock settings from the bucket; see TestPortableBackup",
)
class TestLegacyObjectLock:
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

    def test_om_object_lock_enabled(
        self, ops_manager: MongoDBOpsManager, s3_buckets: Dict[str, str], custom_version: Optional[str]
    ):
        om_tester = ops_manager.get_om_tester()
        lock_only_store = new_om_appdb_s3_store(ops_manager, "s3Store1", s3_buckets["lock"], objectLockEnabled=True)
        retention_store = new_om_appdb_s3_store(
            ops_manager, "s3Store2", s3_buckets["retention"], objectLockEnabled=True
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

        oplog_store = new_om_appdb_s3_store(
            ops_manager,
            "oplogS3Store1",
            s3_buckets["oplog"],
            objectLockEnabled=om_supports_oplog_object_lock(custom_version),
        )
        actual_oplog_store = om_tester.get_oplog_s3_stores()["results"][0]
        # OM's oplog s3Configs endpoint predates object lock support before 8.0.28 and
        # may omit objectLockEnabled entirely there; drop it from the expectation only
        # on those versions so the value is strictly asserted wherever supported.
        if not om_supports_oplog_object_lock(custom_version) and "objectLockEnabled" not in actual_oplog_store:
            del oplog_store["objectLockEnabled"]
        oplog_store = feature_detect_retention(oplog_store, actual_oplog_store)
        om_tester.assert_oplog_s3_stores([oplog_store])


@mark.e2e_om_ops_manager_backup_object_lock
@mark.skipif(
    not BUCKET_DERIVED_OBJECT_LOCK,
    reason="portableBackupEnabled requires Ops Manager 8.0.28+ (8.0.x) or 9.0.2+",
)
class TestPortableBackup:
    def test_om_passes_validations(self, ops_manager: MongoDBOpsManager):
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running,
            timeout=600,
        )

    def test_deprecated_settings_warning(self, ops_manager: MongoDBOpsManager):
        warnings = deprecation_warnings(ops_manager)
        assert len(warnings) == 1, ops_manager.backup_status().get_warnings()
        assert "s3Stores/s3Store1" in warnings[0]
        assert "s3Store2" not in warnings[0]
        assert "oplogS3Store1" not in warnings[0]

    def test_om_reads_object_lock_settings_from_bucket(
        self, ops_manager: MongoDBOpsManager, s3_buckets: Dict[str, str]
    ):
        # Only s3Store1 sets objectLockEnabled on the CR, and the operator does not send it
        # to these versions: every value below comes from the bucket.
        om_tester = ops_manager.get_om_tester()
        bucket_retention = {"objectRetentionDays": OBJECT_RETENTION_DAYS, "objectRetentionMode": OBJECT_RETENTION_MODE}

        om_tester.assert_s3_stores(
            [
                new_om_appdb_s3_store(
                    ops_manager, "s3Store1", s3_buckets["lock"], objectLockEnabled=True, portableBackupEnabled=True
                ),
                new_om_appdb_s3_store(
                    ops_manager,
                    "s3Store2",
                    s3_buckets["retention"],
                    objectLockEnabled=True,
                    portableBackupEnabled=True,
                    **bucket_retention,
                ),
            ]
        )
        lock_only_store = next(s for s in om_tester.get_s3_stores()["results"] if s["id"] == "s3Store1")
        assert lock_only_store.get("objectRetentionMode") is None
        assert lock_only_store.get("objectRetentionDays") is None

        om_tester.assert_oplog_s3_stores(
            [
                new_om_appdb_s3_store(
                    ops_manager,
                    "oplogS3Store1",
                    s3_buckets["oplog"],
                    objectLockEnabled=True,
                    portableBackupEnabled=True,
                    **bucket_retention,
                )
            ]
        )

    def test_removing_deprecated_settings_clears_warning(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        del ops_manager["spec"]["backup"]["s3Stores"][0]["objectLockEnabled"]
        ops_manager.update()

        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=600)
        assert deprecation_warnings(ops_manager) == []

    def test_disabling_portable_backup_fails(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["backup"]["s3Stores"][1]["portableBackupEnabled"] = False
        ops_manager.update()

        ops_manager.backup_status().assert_reaches_phase(
            Phase.Failed,
            # the operator upper-cases the first character of status messages
            msg_regexp='PortableBackupEnabled cannot be set to false for S3 store "s3Store2"',
            timeout=600,
        )

    def test_reenabling_portable_backup_recovers(self, ops_manager: MongoDBOpsManager, s3_buckets: Dict[str, str]):
        ops_manager.load()
        ops_manager["spec"]["backup"]["s3Stores"][1]["portableBackupEnabled"] = True
        ops_manager.update()

        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=600)
        stores = {store["id"]: store for store in ops_manager.get_om_tester().get_s3_stores()["results"]}
        assert stores["s3Store2"]["portableBackupEnabled"] is True
