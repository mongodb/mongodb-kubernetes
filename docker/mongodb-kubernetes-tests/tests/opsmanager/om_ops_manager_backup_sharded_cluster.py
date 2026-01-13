import time
from typing import Optional

from kubernetes.client.rest import ApiException
from kubetester import create_or_update_secret, get_default_storage_class, try_load, wait_until
from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import run_periodically
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.mongotester import MongoTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.opsmanager.om_ops_manager_backup import create_aws_secret, create_s3_bucket
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment
from tests.shardedcluster.conftest import enable_multi_cluster_deployment as enable_multi_cluster_deployment_mdb
from tests.shardedcluster.conftest import get_mongos_service_names

HEAD_PATH = "/head/"
S3_SECRET_NAME = "my-s3-secret"
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
    resource.set_version(custom_mdb_version)

    #  TODO: Remove when CLOUDP-60443 is fixed
    # This test will update oplog to have SCRAM enabled
    # Currently this results in OM failure when enabling backup for a project, backup seems to do some caching resulting in the
    # mongoURI not being updated unless pod is killed. This is documented in CLOUDP-60443, once resolved this skip & comment can be deleted
    resource["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}

    return resource.update()


@fixture(scope="module")
def s3_replica_set(ops_manager, namespace, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=S3_RS_NAME,
    ).configure(ops_manager, "s3metadata")
    resource.set_version(custom_mdb_version)

    return resource.update()


@fixture(scope="module")
def blockstore_replica_set(ops_manager, namespace, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=BLOCKSTORE_RS_NAME,
    ).configure(ops_manager, "blockstore")
    resource.set_version(custom_mdb_version)

    return resource.update()


@fixture(scope="module")
def blockstore_user(namespace, blockstore_replica_set: MongoDB) -> MongoDBUser:
    """Creates a password secret and then the user referencing it"""
    resource = MongoDBUser.from_yaml(yaml_fixture("scram-sha-user-backing-db.yaml"), namespace=namespace)
    resource["spec"]["mongodbResourceRef"]["name"] = blockstore_replica_set.name

    print(f"\nCreating password for MongoDBUser {resource.name} in secret/{resource.get_secret_name()} ")
    create_or_update_secret(
        KubernetesTester.get_namespace(),
        resource.get_secret_name(),
        {
            "password": USER_PASSWORD,
        },
    )

    resource.update()
    return resource


@fixture(scope="module")
def oplog_user(namespace, oplog_replica_set: MongoDB) -> MongoDBUser:
    """Creates a password secret and then the user referencing it"""
    resource = MongoDBUser.from_yaml(
        yaml_fixture("scram-sha-user-backing-db.yaml"),
        namespace=namespace,
        name="mms-user-2",
    )
    resource["spec"]["mongodbResourceRef"]["name"] = oplog_replica_set.name
    resource["spec"]["passwordSecretKeyRef"]["name"] = "mms-user-2-password"
    resource["spec"]["username"] = "mms-user-2"

    print(f"\nCreating password for MongoDBUser {resource.name} in secret/{resource.get_secret_name()} ")
    create_or_update_secret(
        KubernetesTester.get_namespace(),
        resource.get_secret_name(),
        {
            "password": USER_PASSWORD,
        },
    )

    yield resource.update()


@mark.e2e_om_ops_manager_backup_sharded_cluster
class TestOpsManagerCreation:
    """
    name: Ops Manager successful creation with backup and oplog stores enabled
    description: |
      Creates an Ops Manager instance with backup enabled. The OM is expected to get to 'Pending' state
      eventually as it will wait for oplog db to be created
    """

    def test_create_om(
        self,
        ops_manager: MongoDBOpsManager,
        s3_bucket: str,
        custom_version: str,
        custom_appdb_version: str,
    ):
        """creates a s3 bucket, s3 config and an OM resource (waits until Backup gets to Pending state)"""

        ops_manager["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket
        ops_manager["spec"]["backup"]["headDB"]["storageClass"] = get_default_storage_class()
        ops_manager["spec"]["backup"]["members"] = 2

        ops_manager.set_version(custom_version)
        ops_manager.set_appdb_version(custom_appdb_version)
        ops_manager.allow_mdb_rc_versions()

        if is_multi_cluster():
            enable_multi_cluster_deployment(ops_manager)

        ops_manager.update()

        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            msg_regexp="The MongoDB object .+ doesn't exist",
            timeout=900,
        )

    def test_daemon_statefulset(self, ops_manager: MongoDBOpsManager):
        def stateful_sets_become_ready():
            total_ready_replicas = 0
            total_current_replicas = 0
            for member_cluster_name, _ in ops_manager.get_backup_sts_names_in_member_clusters():
                stateful_set = ops_manager.read_backup_statefulset(member_cluster_name=member_cluster_name)
                ready_replicas = (
                    stateful_set.status.ready_replicas if stateful_set.status.ready_replicas is not None else 0
                )
                total_ready_replicas += ready_replicas
                current_replicas = (
                    stateful_set.status.current_replicas if stateful_set.status.current_replicas is not None else 0
                )
                total_current_replicas += current_replicas
            return total_ready_replicas == 2 and total_current_replicas == 2

        KubernetesTester.wait_until(stateful_sets_become_ready, timeout=300)

        for member_cluster_name, _ in ops_manager.get_backup_sts_names_in_member_clusters():
            stateful_set = ops_manager.read_backup_statefulset(member_cluster_name=member_cluster_name)
            # pod template has volume mount request
            assert (HEAD_PATH, "head") in (
                (mount.mount_path, mount.name) for mount in stateful_set.spec.template.spec.containers[0].volume_mounts
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
        """Creates mongodb databases all at once. Concurrent AC modifications may happen from time to time"""
        oplog_replica_set.assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)
        s3_replica_set.assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)
        blockstore_replica_set.assert_reaches_phase(Phase.Running, timeout=600, ignore_errors=True)

    def test_oplog_user_created(self, oplog_user: MongoDBUser):
        oplog_user.assert_reaches_phase(Phase.Updated)

    def test_oplog_updated_scram_sha_enabled(self, oplog_replica_set: MongoDB):
        oplog_replica_set.load()
        oplog_replica_set["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}
        oplog_replica_set.update()
        oplog_replica_set.assert_reaches_phase(Phase.Running)

    def test_om_failed_oplog_no_user_ref(self, ops_manager: MongoDBOpsManager):
        """Waits until Backup is in failed state as blockstore doesn't have reference to the user"""
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Failed,
            msg_regexp=".*is configured to use SCRAM-SHA authentication mode, the user "
            "must be specified using 'mongodbUserRef'",
        )

    def test_fix_om(self, ops_manager: MongoDBOpsManager, oplog_user: MongoDBUser):
        ops_manager.load()
        ops_manager["spec"]["backup"]["opLogStores"][0]["mongodbUserRef"] = {"name": oplog_user.name}
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
    def mdb_latest(self, ops_manager: MongoDBOpsManager, namespace, custom_mdb_version: str):
        resource = MongoDB.from_yaml(
            yaml_fixture("sharded-cluster-for-om.yaml"),
            namespace=namespace,
            name="mdb-four-two",
        ).configure(ops_manager, "firstProject")

        if try_load(resource):
            return resource

        if is_multi_cluster():
            enable_multi_cluster_deployment_mdb(
                resource,
                shard_members_array=[1, 1, 1],
                mongos_members_array=[1, None, None],
                configsrv_members_array=[None, 1, None],
            )

        resource.configure_backup(mode="disabled")
        resource.set_version(ensure_ent_version(custom_mdb_version))
        resource.set_architecture_annotation()

        return resource.update()

    @fixture(scope="class")
    def mdb_prev(self, ops_manager: MongoDBOpsManager, namespace, custom_mdb_prev_version: str):
        resource = MongoDB.from_yaml(
            yaml_fixture("sharded-cluster-for-om.yaml"),
            namespace=namespace,
            name="mdb-four-zero",
        ).configure(ops_manager, "secondProject")

        if try_load(resource):
            return resource

        if is_multi_cluster():
            enable_multi_cluster_deployment_mdb(
                resource,
                shard_members_array=[1, 1, 1],
                mongos_members_array=[1, None, None],
                configsrv_members_array=[None, 1, None],
            )

        resource.configure_backup(mode="disabled")
        resource.set_version(ensure_ent_version(custom_mdb_prev_version))
        resource.set_architecture_annotation()

        return resource.update()

    @fixture(scope="class")
    def mdb_latest_tester(self, mdb_latest: MongoDB) -> MongoTester:
        service_names = get_mongos_service_names(mdb_latest)

        return mdb_latest.tester(service_names=service_names)

    @fixture(scope="class")
    def mdb_prev_tester(self, mdb_prev: MongoDB) -> MongoTester:
        service_names = get_mongos_service_names(mdb_prev)

        return mdb_prev.tester(service_names=service_names)

    def test_mdbs_created(self, mdb_latest: MongoDB, mdb_prev: MongoDB):
        mdb_latest.assert_reaches_phase(Phase.Running, timeout=1200)
        mdb_prev.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_mdbs_enable_backup(
        self, mdb_latest: MongoDB, mdb_prev: MongoDB, mdb_latest_tester: MongoTester, mdb_prev_tester: MongoTester
    ):
        def until_shards_are_here():
            shards_latest = mdb_latest_tester.client.admin.command("listShards")
            shards_prev = mdb_prev_tester.client.admin.command("listShards")
            if len(shards_latest["shards"]) == 2 and len(shards_prev["shards"]) == 2:
                return True
            else:
                print(f"shards are not configured yet: latest: {shards_latest} / prev: {shards_prev}")
            return False

        wait_until(until_shards_are_here, 600)

        # we need to sleep here to give OM some time to recognize the shards.
        # otherwise, if you start a backup during a topology change will lead the backup to be aborted.
        time.sleep(30)
        mdb_latest.load()
        mdb_latest.configure_backup(mode="enabled")
        mdb_latest.update()

        mdb_prev.load()
        mdb_prev.configure_backup(mode="enabled")
        mdb_prev.update()

        mdb_prev.assert_reaches_phase(Phase.Running, timeout=1200)
        mdb_latest.assert_reaches_phase(Phase.Running, timeout=1200)

    def test_mdbs_backuped(self, ops_manager: MongoDBOpsManager):
        om_tester_first = ops_manager.get_om_tester(project_name="firstProject")
        om_tester_second = ops_manager.get_om_tester(project_name="secondProject")

        # wait until a first snapshot is ready for both
        om_tester_first.wait_until_backup_snapshots_are_ready(
            expected_count=1, expected_config_count=4, is_sharded_cluster=True, timeout=1600
        )
        om_tester_second.wait_until_backup_snapshots_are_ready(
            expected_count=1, expected_config_count=4, is_sharded_cluster=True, timeout=1600
        )

    def test_can_transition_from_started_to_stopped(self, mdb_latest: MongoDB, mdb_prev: MongoDB):
        # a direction transition from enabled to disabled is a single
        # step for the operator
        mdb_prev.assert_backup_reaches_status("STARTED", timeout=100)
        mdb_prev.configure_backup(mode="disabled")
        mdb_prev.update()
        mdb_prev.assert_backup_reaches_status("STOPPED", timeout=1600)

    def test_can_transition_from_started_to_terminated_0(self, mdb_latest: MongoDB, mdb_prev: MongoDB):
        # a direct transition from enabled to terminated is not possible
        # the operator should handle the transition from STARTED -> STOPPED -> TERMINATING
        mdb_latest.assert_backup_reaches_status("STARTED", timeout=100)
        mdb_latest.configure_backup(mode="terminated")
        mdb_latest.update()
        mdb_latest.assert_backup_reaches_status("TERMINATING", timeout=1600)

    def test_backup_terminated_for_deleted_resource(self, ops_manager: MongoDBOpsManager, mdb_prev: MongoDB):
        # re-enable backup
        mdb_prev.configure_backup(mode="enabled")
        mdb_prev["spec"]["backup"]["autoTerminateOnDeletion"] = True
        mdb_prev.update()
        mdb_prev.assert_backup_reaches_status("STARTED", timeout=1600)
        mdb_prev.delete()

        def resource_is_deleted() -> bool:
            try:
                mdb_prev.load()
                return False
            except ApiException:
                return True

        # wait until the resource is deleted
        run_periodically(resource_is_deleted, timeout=300)

        om_tester_second = ops_manager.get_om_tester(project_name="secondProject")
        om_tester_second.wait_until_backup_snapshots_are_ready(
            expected_count=0, expected_config_count=4, is_sharded_cluster=True
        )
        om_tester_second.wait_until_backup_deactivated(is_sharded_cluster=True, expected_config_count=4)

    def test_hosts_were_removed(self, ops_manager: MongoDBOpsManager, mdb_prev: MongoDB):
        om_tester_second = ops_manager.get_om_tester(project_name="secondProject")
        om_tester_second.wait_until_hosts_are_empty()

    # Note: as of right now, we cannot deploy the same mdb again, because we will run into the error: Backup failed
    # to start: Config server <hostname>: 27017 has no startup parameters.
