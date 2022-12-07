from operator import attrgetter
from typing import Optional, Dict, Callable

from kubernetes import client
from kubernetes.client import ApiException
from pytest import mark, fixture

from kubetester import (
    assert_pod_container_security_context,
    assert_pod_security_context,
    run_periodically,
)
from kubetester import get_default_storage_class, create_or_update, try_load
from kubetester.awss3client import AwsS3Client, s3_endpoint
from kubetester.kubetester import (
    skip_if_local,
    fixture as yaml_fixture,
    KubernetesTester,
    running_locally,
)
from kubetester.mongodb import Phase, MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.test_identifiers import set_test_identifier
from tests.opsmanager.backup_snapshot_schedule_tests import BackupSnapshotScheduleTests
from tests.opsmanager.conftest import ensure_ent_version

HEAD_PATH = "/head/"
S3_SECRET_NAME = "my-s3-secret"
AWS_REGION = "us-east-1"
OPLOG_RS_NAME = "my-mongodb-oplog"
S3_RS_NAME = "my-mongodb-s3"
BLOCKSTORE_RS_NAME = "my-mongodb-blockstore"
USER_PASSWORD = "/qwerty@!#:"

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
    }


def new_om_data_store(
    mdb: MongoDB,
    id: str,
    assignment_enabled: bool = True,
    user_name: Optional[str] = None,
    password: Optional[str] = None,
) -> Dict:
    return {
        "id": id,
        "uri": mdb.mongo_uri(user_name=user_name, password=password),
        "ssl": mdb.is_tls_enabled(),
        "assignmentEnabled": assignment_enabled,
    }


@fixture(scope="module")
def s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> str:
    create_aws_secret(aws_s3_client, S3_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client, "test-bucket-s3")


def create_aws_secret(aws_s3_client, secret_name: str, namespace: str):
    KubernetesTester.create_secret(
        namespace,
        secret_name,
        {
            "accessKey": aws_s3_client.aws_access_key,
            "secretKey": aws_s3_client.aws_secret_access_key,
        },
    )
    print("\nCreated a secret for S3 credentials", secret_name)


def create_s3_bucket(aws_s3_client, bucket_prefix: str = "test-bucket-"):
    """creates a s3 bucket and a s3 config"""
    bucket_name = set_test_identifier(
        f"create_s3_bucket_{bucket_prefix}",
        KubernetesTester.random_k8s_name(bucket_prefix),
    )

    try:
        aws_s3_client.create_s3_bucket(bucket_name)
        print("Created S3 bucket", bucket_name)

        yield bucket_name
        if not running_locally():
            print("\nRemoving S3 bucket", bucket_name)
            aws_s3_client.delete_s3_bucket(bucket_name)
    except Exception as e:
        if running_locally():
            print(
                f"Local run: skipping creating bucket {bucket_name} because it already exists."
            )
            yield bucket_name
        else:
            raise e


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

    yield create_or_update(resource)


@fixture(scope="module")
def s3_replica_set(ops_manager, namespace) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=S3_RS_NAME,
    ).configure(ops_manager, "s3metadata")

    yield create_or_update(resource)


@fixture(scope="module")
def blockstore_replica_set(ops_manager, namespace, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=BLOCKSTORE_RS_NAME,
    ).configure(ops_manager, "blockstore")
    resource["spec"]["version"] = custom_mdb_version
    yield create_or_update(resource)


@fixture(scope="module")
def blockstore_user(namespace, blockstore_replica_set: MongoDB) -> MongoDBUser:
    """Creates a password secret and then the user referencing it"""
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

    yield create_or_update(resource)


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

    yield create_or_update(resource)


@mark.e2e_om_ops_manager_backup
class TestOpsManagerCreation:
    """
    name: Ops Manager successful creation with backup and oplog stores enabled
    description: |
      Creates an Ops Manager instance with backup enabled. The OM is expected to get to 'Pending' state
      eventually as it will wait for oplog db to be created
    """

    def test_setup_gp2_storage_class(self):
        """This is necessary for Backup HeadDB"""
        KubernetesTester.make_default_gp2_storage_class()

    def test_create_om(self, ops_manager: MongoDBOpsManager, s3_bucket: str, custom_version: str, custom_appdb_version: str):
        """creates a s3 bucket, s3 config and an OM resource (waits until Backup gets to Pending state)"""

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

    def test_daemon_pvc(self, ops_manager: MongoDBOpsManager, namespace: str):
        """Verifies the PVCs mounted to the pod"""
        pods = ops_manager.read_backup_pods()
        idx = 0
        for pod in pods:
            claims = [
                volume
                for volume in pod.spec.volumes
                if getattr(volume, "persistent_volume_claim")
            ]
            assert len(claims) == 1
            claims.sort(key=attrgetter("name"))

            default_sc = get_default_storage_class()
            KubernetesTester.check_single_pvc(
                namespace,
                claims[0],
                "head",
                "head-{}-{}".format(ops_manager.backup_daemon_name(), idx),
                "500M",
                default_sc,
            )
            idx += 1

    def test_backup_daemon_services_created(self, namespace):
        """Backup creates two additional services for queryable backup"""
        services = client.CoreV1Api().list_namespaced_service(namespace).items

        # If running locally in 'default' namespace, there might be more
        # services on it. Let's make sure we only count those that we care of.
        # For now we allow this test to fail, because it is too broad to be significant
        # and it is easy to break it.
        backup_services = [
            s for s in services if s.metadata.name.startswith("om-backup")
        ]

        assert len(backup_services) >= 3

    @skip_if_local
    def test_om(self, ops_manager: MongoDBOpsManager):
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        for pod_fqdn in ops_manager.backup_daemon_pods_fqdns():
            om_tester.assert_daemon_enabled(pod_fqdn, HEAD_PATH)
        # No oplog stores were created in Ops Manager by this time
        om_tester.assert_oplog_stores([])
        om_tester.assert_s3_stores([])

    def test_generations(self, ops_manager: MongoDBOpsManager):
        assert ops_manager.appdb_status().get_observed_generation() == 1
        assert ops_manager.om_status().get_observed_generation() == 1
        assert ops_manager.backup_status().get_observed_generation() == 1

    def test_security_contexts_appdb(
        self,
        ops_manager: MongoDBOpsManager,
        operator_installation_config: Dict[str, str],
    ):
        managed = operator_installation_config["managedSecurityContext"] == "true"
        for pod in ops_manager.read_appdb_pods():
            assert_pod_security_context(pod, managed)
            assert_pod_container_security_context(pod.spec.containers[0], managed)

    def test_security_contexts_om(
        self,
        ops_manager: MongoDBOpsManager,
        operator_installation_config: Dict[str, str],
    ):
        managed = operator_installation_config["managedSecurityContext"] == "true"
        for pod in ops_manager.read_om_pods():
            assert_pod_security_context(pod, managed)
            assert_pod_container_security_context(pod.spec.containers[0], managed)


@mark.e2e_om_ops_manager_backup
class TestBackupDatabasesAdded:
    """name: Creates three mongodb resources for oplog, s3 and blockstore and waits until OM resource gets to
    running state"""

    def test_backup_mdbs_created(
        self,
        oplog_replica_set: MongoDB,
        s3_replica_set: MongoDB,
        blockstore_replica_set: MongoDB,
    ):
        """Creates mongodb databases all at once"""
        oplog_replica_set.assert_reaches_phase(Phase.Running)
        s3_replica_set.assert_reaches_phase(Phase.Running)
        blockstore_replica_set.assert_reaches_phase(Phase.Running)

    def test_oplog_user_created(self, oplog_user: MongoDBUser):
        oplog_user.assert_reaches_phase(Phase.Updated)

    def test_oplog_updated_scram_sha_enabled(self, oplog_replica_set: MongoDB):
        oplog_replica_set.load()
        oplog_replica_set["spec"]["security"] = {
            "authentication": {"enabled": True, "modes": ["SCRAM"]}
        }
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

    @skip_if_local
    def test_om(
        self,
        s3_bucket: str,
        aws_s3_client: AwsS3Client,
        ops_manager: MongoDBOpsManager,
        oplog_replica_set: MongoDB,
        s3_replica_set: MongoDB,
        blockstore_replica_set: MongoDB,
        oplog_user: MongoDBUser,
    ):
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        # Nothing has changed for daemon

        for pod_fqdn in ops_manager.backup_daemon_pods_fqdns():
            om_tester.assert_daemon_enabled(pod_fqdn, HEAD_PATH)

        om_tester.assert_block_stores(
            [new_om_data_store(blockstore_replica_set, "blockStore1")]
        )
        # oplog store has authentication enabled
        om_tester.assert_oplog_stores(
            [
                new_om_data_store(
                    oplog_replica_set,
                    "oplog1",
                    user_name=oplog_user.get_user_name(),
                    password=USER_PASSWORD,
                )
            ]
        )
        om_tester.assert_s3_stores(
            [new_om_s3_store(s3_replica_set, "s3Store1", s3_bucket, aws_s3_client)]
        )

    def test_generations(self, ops_manager: MongoDBOpsManager):
        """There have been an update to the OM spec - all observed generations are expected to be updated"""
        assert ops_manager.appdb_status().get_observed_generation() == 2
        assert ops_manager.om_status().get_observed_generation() == 2
        assert ops_manager.backup_status().get_observed_generation() == 2

    def test_security_contexts_backup(
        self,
        ops_manager: MongoDBOpsManager,
        operator_installation_config: Dict[str, str],
    ):
        managed = operator_installation_config["managedSecurityContext"] == "true"
        pods = ops_manager.read_backup_pods()
        for pod in pods:
            assert_pod_security_context(pod, managed)
            assert_pod_container_security_context(pod.spec.containers[0], managed)


@mark.e2e_om_ops_manager_backup
class TestOpsManagerWatchesBlockStoreUpdates:
    def test_om_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=40)

    def test_scramsha_enabled_for_blockstore(self, blockstore_replica_set: MongoDB):
        """Enables SCRAM for the blockstore replica set. Note that until CLOUDP-67736 is fixed
        the order of operations (scram first, MongoDBUser - next) is important"""
        blockstore_replica_set["spec"]["security"] = {
            "authentication": {"enabled": True, "modes": ["SCRAM"]}
        }
        blockstore_replica_set.update()

        # timeout of 600 is required when enabling SCRAM in mdb5.0.0
        blockstore_replica_set.assert_reaches_phase(Phase.Running, timeout=900)

    def test_blockstore_user_was_added_to_om(self, blockstore_user: MongoDBUser):
        blockstore_user.assert_reaches_phase(Phase.Updated)

    def test_om_failed_oplog_no_user_ref(self, ops_manager: MongoDBOpsManager):
        """Waits until Ops manager is in failed state as blockstore doesn't have reference to the user"""
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Failed,
            msg_regexp=".*is configured to use SCRAM-SHA authentication mode, the user "
            "must be specified using 'mongodbUserRef'",
        )

    def test_fix_om(self, ops_manager: MongoDBOpsManager, blockstore_user: MongoDBUser):
        ops_manager.load()
        ops_manager["spec"]["backup"]["blockStores"][0]["mongodbUserRef"] = {
            "name": blockstore_user.name
        }
        ops_manager.update()
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running,
            timeout=200,
            ignore_errors=True,
        )
        assert ops_manager.backup_status().get_message() is None

    @skip_if_local
    def test_om(
        self,
        s3_bucket: str,
        aws_s3_client: AwsS3Client,
        ops_manager: MongoDBOpsManager,
        oplog_replica_set: MongoDB,
        s3_replica_set: MongoDB,
        blockstore_replica_set: MongoDB,
        oplog_user: MongoDBUser,
        blockstore_user: MongoDBUser,
    ):
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        # Nothing has changed for daemon
        for pod_fqdn in ops_manager.backup_daemon_pods_fqdns():
            om_tester.assert_daemon_enabled(pod_fqdn, HEAD_PATH)

        # block store has authentication enabled
        om_tester.assert_block_stores(
            [
                new_om_data_store(
                    blockstore_replica_set,
                    "blockStore1",
                    user_name=blockstore_user.get_user_name(),
                    password=USER_PASSWORD,
                )
            ]
        )

        # oplog has authentication enabled
        om_tester.assert_oplog_stores(
            [
                new_om_data_store(
                    oplog_replica_set,
                    "oplog1",
                    user_name=oplog_user.get_user_name(),
                    password=USER_PASSWORD,
                )
            ]
        )
        om_tester.assert_s3_stores(
            [new_om_s3_store(s3_replica_set, "s3Store1", s3_bucket, aws_s3_client)]
        )


@mark.e2e_om_ops_manager_backup
class TestBackupForMongodb:
    """This part ensures that backup for the client works correctly and the snapshot is created.
    Both latest and the one before the latest are tested (as the backup process for them may differ significantly)"""

    @fixture(scope="class")
    def mdb_latest(
        self, ops_manager: MongoDBOpsManager, namespace, custom_mdb_version: str
    ):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name="mdb-four-two",
        ).configure(ops_manager, "firstProject")
        resource["spec"]["version"] = ensure_ent_version(custom_mdb_version)
        resource.configure_backup(mode="disabled")
        return create_or_update(resource)

    @fixture(scope="class")
    def mdb_prev(
        self, ops_manager: MongoDBOpsManager, namespace, custom_mdb_prev_version: str
    ):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name="mdb-four-zero",
        ).configure(ops_manager, "secondProject")
        resource["spec"]["version"] = ensure_ent_version(custom_mdb_prev_version)
        resource.configure_backup(mode="disabled")
        return create_or_update(resource)

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
        om_tester_first.wait_until_backup_snapshots_are_ready(expected_count=1)
        om_tester_second.wait_until_backup_snapshots_are_ready(expected_count=1)

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

    def test_backup_terminated_for_deleted_resource(
        self, ops_manager: MongoDBOpsManager, mdb_prev: MongoDB
    ):
        # re-enable backup
        mdb_prev.configure_backup(mode="enabled")
        mdb_prev["spec"]["backup"]["autoTerminateOnDeletion"] = True
        mdb_prev.update()
        mdb_prev.assert_backup_reaches_status("STARTED", timeout=600)
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
        om_tester_second.wait_until_backup_snapshots_are_ready(expected_count=0)


@mark.e2e_om_ops_manager_backup
class TestBackupSnapshotSchedule(BackupSnapshotScheduleTests):
    pass


@mark.e2e_om_ops_manager_backup
class TestBackupConfigurationAdditionDeletion:
    def test_oplog_store_is_added(
        self,
        ops_manager: MongoDBOpsManager,
        s3_bucket: str,
        aws_s3_client: AwsS3Client,
        oplog_replica_set: MongoDB,
        s3_replica_set: MongoDB,
        oplog_user: MongoDBUser,
    ):
        ops_manager.reload()
        ops_manager["spec"]["backup"]["opLogStores"].append(
            {"name": "oplog2", "mongodbResourceRef": {"name": S3_RS_NAME}}
        )

        ops_manager.update()
        ops_manager.backup_status().assert_abandons_phase(Phase.Running, timeout=60)
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=600)

        om_tester = ops_manager.get_om_tester()
        om_tester.assert_oplog_stores(
            [
                new_om_data_store(
                    oplog_replica_set,
                    "oplog1",
                    user_name=oplog_user.get_user_name(),
                    password=USER_PASSWORD,
                ),
                new_om_data_store(s3_replica_set, "oplog2"),
            ]
        )
        om_tester.assert_s3_stores(
            [new_om_s3_store(s3_replica_set, "s3Store1", s3_bucket, aws_s3_client)]
        )

    def test_oplog_store_is_deleted_correctly(
        self,
        ops_manager: MongoDBOpsManager,
        s3_bucket: str,
        aws_s3_client: AwsS3Client,
        oplog_replica_set: MongoDB,
        s3_replica_set: MongoDB,
        blockstore_replica_set: MongoDB,
        blockstore_user: MongoDBUser,
        oplog_user: MongoDBUser,
    ):
        ops_manager.reload()
        ops_manager["spec"]["backup"]["opLogStores"].pop()
        ops_manager.update()

        ops_manager.backup_status().assert_abandons_phase(Phase.Running, timeout=60)
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=600)

        om_tester = ops_manager.get_om_tester()

        om_tester.assert_oplog_stores(
            [
                new_om_data_store(
                    oplog_replica_set,
                    "oplog1",
                    user_name=oplog_user.get_user_name(),
                    password=USER_PASSWORD,
                )
            ]
        )
        om_tester.assert_s3_stores(
            [new_om_s3_store(s3_replica_set, "s3Store1", s3_bucket, aws_s3_client)]
        )
        om_tester.assert_block_stores(
            [
                new_om_data_store(
                    blockstore_replica_set,
                    "blockStore1",
                    user_name=blockstore_user.get_user_name(),
                    password=USER_PASSWORD,
                )
            ]
        )

    def test_error_on_s3store_removal(
        self,
        ops_manager: MongoDBOpsManager,
    ):
        """Removing the s3 store when there are backups running is an error"""
        ops_manager.reload()
        ops_manager["spec"]["backup"]["s3Stores"] = []
        ops_manager.update()

        try:
            ops_manager.backup_status().assert_reaches_phase(
                Phase.Failed,
                msg_regexp=".*BACKUP_CANNOT_REMOVE_S3_STORE_CONFIG.*",
                timeout=200,
            )
        except Exception:
            # Some backup internal logic: if more than 1 snapshot stores are configured in OM (CM?) then
            # the random one will be picked for backup of mongodb resource.
            # There's no way to specify the config to be used when enabling backup for a mongodb deployment.
            # So this means that either the removal of s3 will fail as it's used already or it will succeed as
            # the blockstore snapshot was used instead and the OM would get to Running phase
            assert ops_manager.backup_status().get_phase() == Phase.Running
