from typing import Optional

import tests.opsmanager.om_ops_manager_backup_sharded_cluster
from kubetester import get_default_storage_class, try_load, create_or_update
from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import (
    fixture as yaml_fixture,
    KubernetesTester,
)
from kubetester.mongodb import Phase, MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.opsmanager import MongoDBOpsManager
from pytest import mark, fixture

from tests.opsmanager.backup_snapshot_schedule_tests import BackupSnapshotScheduleTests
from tests.opsmanager.conftest import ensure_ent_version
from tests.opsmanager.om_ops_manager_backup import (
    create_aws_secret,
    create_s3_bucket,
)

HEAD_PATH = "/head/"
S3_SECRET_NAME = "my-s3-secret"
AWS_REGION = "us-east-1"
OPLOG_RS_NAME = "my-mongodb-oplog"
S3_RS_NAME = "my-mongodb-s3"
BLOCKSTORE_RS_NAME = "my-mongodb-blockstore"
USER_PASSWORD = "/qwerty@!#:"


@fixture(scope="module")
def s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> str:
    create_aws_secret(aws_s3_client, S3_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, "test-bucket-sharded-")


@fixture(scope="module")
def ops_manager(
    namespace: str,
    s3_bucket: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup.yaml"), namespace=namespace
    )

    try_load(resource)
    return resource


@fixture(scope="module")
def oplog_replica_set(ops_manager, namespace, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=OPLOG_RS_NAME,
    ).configure(ops_manager, "development")
    resource["spec"]["version"] = custom_mdb_version

    #  TODO: Remove when CLOUDP-60443 is fixed
    # This test will update oplog to have SCRAM enabled
    # Currently this results in OM failure when enabling backup for a project, backup seems to do some caching resulting in the
    # mongoURI not being updated unless pod is killed. This is documented in CLOUDP-60443, once resolved this skip & comment can be deleted
    resource["spec"]["security"] = {
        "authentication": {"enabled": True, "modes": ["SCRAM"]}
    }

    create_or_update(resource)
    return resource


@fixture(scope="module")
def s3_replica_set(ops_manager, namespace) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=S3_RS_NAME,
    ).configure(ops_manager, "s3metadata")

    create_or_update(resource)
    return resource


@fixture(scope="module")
def blockstore_replica_set(ops_manager, namespace, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=BLOCKSTORE_RS_NAME,
    ).configure(ops_manager, "blockstore")
    resource["spec"]["version"] = custom_mdb_version
    create_or_update(resource)
    return resource


@fixture(scope="module")
def blockstore_user(namespace, blockstore_replica_set: MongoDB) -> MongoDBUser:
    """ Creates a password secret and then the user referencing it"""
    resource = MongoDBUser.from_yaml(
        yaml_fixture("scram-sha-user-backing-db.yaml"), namespace=namespace
    )
    resource["spec"]["mongodbResourceRef"]["name"] = blockstore_replica_set.name

    print(
        f"\nCreating password for MongoDBUser {resource.name} in secret/{resource.get_secret_name()} "
    )
    KubernetesTester.create_secret(
        KubernetesTester.get_namespace(),
        resource.get_secret_name(),
        {
            "password": USER_PASSWORD,
        },
    )

    create_or_update(resource)
    return resource


@fixture(scope="module")
def oplog_user(namespace, oplog_replica_set: MongoDB) -> MongoDBUser:
    """ Creates a password secret and then the user referencing it"""
    resource = MongoDBUser.from_yaml(
        yaml_fixture("scram-sha-user-backing-db.yaml"),
        namespace=namespace,
        name="mms-user-2",
    )
    resource["spec"]["mongodbResourceRef"]["name"] = oplog_replica_set.name
    resource["spec"]["passwordSecretKeyRef"]["name"] = "mms-user-2-password"
    resource["spec"]["username"] = "mms-user-2"

    print(
        f"\nCreating password for MongoDBUser {resource.name} in secret/{resource.get_secret_name()} "
    )
    KubernetesTester.create_secret(
        KubernetesTester.get_namespace(),
        resource.get_secret_name(),
        {
            "password": USER_PASSWORD,
        },
    )

    create_or_update(resource)
    return resource


@mark.e2e_om_ops_manager_backup_sharded_cluster
class TestOpsManagerCreation:
    """
    name: Ops Manager successful creation with backup and oplog stores enabled
    description: |
      Creates an Ops Manager instance with backup enabled. The OM is expected to get to 'Pending' state
      eventually as it will wait for oplog db to be created
    """

    def test_setup_gp2_storage_class(self):
        """ This is necessary for Backup HeadDB """
        KubernetesTester.make_default_gp2_storage_class()

    def test_create_om(self, ops_manager: MongoDBOpsManager, s3_bucket: str, custom_version: str, custom_appdb_version: str):
        """ creates a s3 bucket, s3 config and an OM resource (waits until Backup gets to Pending state)"""

        ops_manager["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket
        ops_manager["spec"]["backup"]["headDB"]["storageClass"] = get_default_storage_class()
        ops_manager["spec"]["backup"]["members"] = 2

        ops_manager.set_version(custom_version)
        ops_manager.set_appdb_version(custom_appdb_version)
        ops_manager.allow_mdb_rc_versions()

        create_or_update(ops_manager)

        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            msg_regexp="The MongoDB object .+ doesn't exist",
            timeout=900,
        )

    def test_daemon_statefulset(self, ops_manager: MongoDBOpsManager):
        def stateful_set_becomes_ready():
            stateful_set = ops_manager.read_backup_statefulset()
            return (
                stateful_set.status.ready_replicas == 2
                and stateful_set.status.current_replicas == 2
            )

        KubernetesTester.wait_until(stateful_set_becomes_ready, timeout=300)

        stateful_set = ops_manager.read_backup_statefulset()
        # pod template has volume mount request
        assert (HEAD_PATH, "head") in (
            (mount.mount_path, mount.name)
            for mount in stateful_set.spec.template.spec.containers[0].volume_mounts
        )


@mark.e2e_om_ops_manager_backup_sharded_cluster
class TestBackupDatabasesAdded:
    """name: Creates three mongodb resources for oplog, s3 and blockstore and waits until OM resource gets to
    running state"""

    def test_backup_mdbs_created(
        self,
        oplog_replica_set: MongoDB,
        s3_replica_set: MongoDB,
        blockstore_replica_set: MongoDB,
    ):
        """ Creates mongodb databases all at once """
        oplog_replica_set.assert_reaches_phase(Phase.Running)
        s3_replica_set.assert_reaches_phase(Phase.Running)
        blockstore_replica_set.assert_reaches_phase(Phase.Running)

    def test_oplog_user_created(self, oplog_user: MongoDBUser):
        oplog_user.assert_reaches_phase(Phase.Updated)

    def test_oplog_updated_scram_sha_enabled(self, oplog_replica_set: MongoDB):
        oplog_replica_set["spec"]["security"] = {
            "authentication": {"enabled": True, "modes": ["SCRAM"]}
        }
        oplog_replica_set.update()
        oplog_replica_set.assert_reaches_phase(Phase.Running)

    def test_om_failed_oplog_no_user_ref(self, ops_manager: MongoDBOpsManager):
        """ Waits until Backup is in failed state as blockstore doesn't have reference to the user"""
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Failed,
            msg_regexp=".*is configured to use SCRAM-SHA authentication mode, the user "
            "must be specified using 'mongodbUserRef'",
        )

    def test_fix_om(self, ops_manager: MongoDBOpsManager, oplog_user: MongoDBUser):
        ops_manager.load()
        ops_manager["spec"]["backup"]["opLogStores"][0]["mongodbUserRef"] = {
            "name": oplog_user.name
        }
        ops_manager.update()

        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running,
            timeout=200,
            ignore_errors=True,
        )

        assert ops_manager.backup_status().get_message() is None


@mark.e2e_om_ops_manager_backup_sharded_cluster
class TestBackupForMongodb:
    """This part ensures that backup for the client works correctly and the snapshot is created.
    Both latest and the one before the latest are tested (as the backup process for them may differ significantly)"""

    @fixture(scope="class")
    def mdb_latest(
        self, ops_manager: MongoDBOpsManager, namespace, custom_mdb_version: str
    ):
        resource = MongoDB.from_yaml(
            yaml_fixture("sharded-cluster-for-om.yaml"),
            namespace=namespace,
            name="mdb-four-two",
        ).configure(ops_manager, "firstProject")
        resource["spec"]["version"] = ensure_ent_version(custom_mdb_version)
        resource.configure_backup(mode="disabled")
        create_or_update(resource)
        return resource

    @fixture(scope="class")
    def mdb_prev(
        self, ops_manager: MongoDBOpsManager, namespace, custom_mdb_prev_version: str
    ):
        resource = MongoDB.from_yaml(
            yaml_fixture("sharded-cluster-for-om.yaml"),
            namespace=namespace,
            name="mdb-four-zero",
        ).configure(ops_manager, "secondProject")
        resource["spec"]["version"] = ensure_ent_version(custom_mdb_prev_version)
        resource.configure_backup(mode="disabled")
        create_or_update(resource)
        return resource

    def test_mdbs_created(self, mdb_latest: MongoDB, mdb_prev: MongoDB):
        mdb_latest.assert_reaches_phase(Phase.Running)
        mdb_prev.assert_reaches_phase(Phase.Running)

    def test_mdbs_enable_backup(self, mdb_latest: MongoDB, mdb_prev: MongoDB):
        mdb_latest.load()
        mdb_latest.configure_backup(mode="enabled")
        mdb_latest.update()
        mdb_prev.load()
        mdb_prev.configure_backup(mode="enabled")
        mdb_prev.update()
        mdb_prev.assert_reaches_phase(Phase.Running)
        mdb_latest.assert_reaches_phase(Phase.Running)

    def test_mdbs_backuped(self, ops_manager: MongoDBOpsManager):
        om_tester_first = ops_manager.get_om_tester(project_name="firstProject")
        om_tester_second = ops_manager.get_om_tester(project_name="secondProject")

        # wait until a first snapshot is ready for both
        om_tester_first.wait_until_backup_snapshots_are_ready(
            expected_count=1, expected_config_count=4
        )
        om_tester_second.wait_until_backup_snapshots_are_ready(
            expected_count=1, expected_config_count=4
        )

    def test_can_transition_from_started_to_stopped(
        self, mdb_latest: MongoDB, mdb_prev: MongoDB
    ):
        # a direction transition from enabled to disabled is a single
        # step for the operator
        mdb_prev.assert_backup_reaches_status("STARTED", timeout=100)
        mdb_prev.configure_backup(mode="disabled")
        mdb_prev.update()
        mdb_prev.assert_backup_reaches_status("STOPPED", timeout=600)

    def test_can_transition_from_started_to_terminated_0(
        self, mdb_latest: MongoDB, mdb_prev: MongoDB
    ):
        # a direct transition from enabled to terminated is not possible
        # the operator should handle the transition from STARTED -> STOPPED -> TERMINATING
        mdb_latest.assert_backup_reaches_status("STARTED", timeout=100)
        mdb_latest.configure_backup(mode="terminated")
        mdb_latest.update()
        mdb_latest.assert_backup_reaches_status("TERMINATING", timeout=600)


# This test extends om_ops_manager_backup.TestBackupSnapshotSchedule tests but overrides fixtures
# to run snapshot schedule tests on sharded MongoDB 3.6.
# Additionally, it tests clusterCheckpointInterval field.
@mark.e2e_om_ops_manager_backup_sharded_cluster
class TestBackupSnapshotScheduleOnMongoDB36(BackupSnapshotScheduleTests):
    @fixture
    def mdb(self, ops_manager: MongoDBOpsManager):
        resource = MongoDB.from_yaml(
            yaml_fixture("sharded-cluster-for-om.yaml"),
            namespace=ops_manager.namespace,
            name="mdb-backup-snapshot-schedule-sharded-on-36",
        )

        try_load(resource)

        return resource

    @fixture
    def om_project_name(self):
        return "backupSnapshotScheduleSharded36"

    @fixture
    def mdb_version(self):
        return "3.6.20"

    def test_cluster_checkpoint_interval(self, mdb: MongoDB):
        self.update_snapshot_schedule(mdb, {
            "clusterCheckpointIntervalMin": 60,
        })

        self.assert_snapshot_schedule_in_ops_manager(mdb.get_om_tester(), {
            "clusterCheckpointIntervalMin": 60,
        })
