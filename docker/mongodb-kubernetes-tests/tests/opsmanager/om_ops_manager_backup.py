from operator import attrgetter
from typing import Dict, Optional

from kubernetes import client
from kubernetes.client import ApiException
from kubetester import (
    assert_pod_container_security_context,
    assert_pod_security_context,
    create_or_update_configmap,
    create_or_update_secret,
    get_default_storage_class,
    run_periodically,
    try_load,
)
from kubetester.awss3client import AwsS3Client, s3_endpoint
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import running_locally, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from kubetester.test_identifiers import set_test_identifier
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.constants import AWS_REGION
from tests.opsmanager.backup_snapshot_schedule_tests import BackupSnapshotScheduleTests
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

HEAD_PATH = "/head/"
S3_SECRET_NAME = "my-s3-secret"
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


def create_aws_secret(
    aws_s3_client,
    secret_name: str,
    namespace: str,
    api_client: Optional[client.ApiClient] = None,
):
    create_or_update_secret(
        namespace,
        secret_name,
        {
            "accessKey": aws_s3_client.aws_access_key,
            "secretKey": aws_s3_client.aws_secret_access_key,
        },
        api_client=api_client,
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
            try:
                aws_s3_client.delete_s3_bucket(bucket_name)
            except Exception as e:
                print(
                    f"Caught exception while removing S3 bucket {bucket_name}, but ignoring to not obscure the test result: {e}"
                )
    except Exception as e:
        if running_locally():
            print(f"Local run: skipping creating bucket {bucket_name} because it already exists.")
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
    ).configure(ops_manager)
    resource.set_version(custom_mdb_version)

    #  TODO: Remove when CLOUDP-60443 is fixed
    # This test will update oplog to have SCRAM enabled
    # Currently this results in OM failure when enabling backup for a project, backup seems to do some caching resulting in the
    # mongoURI not being updated unless pod is killed. This is documented in CLOUDP-60443, once resolved this skip & comment can be deleted
    resource["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}

    resource.update()
    return resource


@fixture(scope="module")
def s3_replica_set(ops_manager, namespace, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=S3_RS_NAME,
    ).configure(ops_manager)

    resource.set_version(custom_mdb_version)
    resource.update()
    return resource


@fixture(scope="module")
def blockstore_replica_set(ops_manager, namespace, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=BLOCKSTORE_RS_NAME,
    ).configure(ops_manager)
    resource.set_version(custom_mdb_version)
    resource.update()
    return resource


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

    resource.update()
    return resource


@mark.e2e_om_ops_manager_backup
class TestOpsManagerCreation:
    """
    name: Ops Manager successful creation with backup and oplog stores enabled
    description: |
      Creates an Ops Manager instance with backup enabled. The OM is expected to get to 'Pending' state
      eventually as it will wait for oplog db to be created
    """

    def test_create_access_logback_xml_configmap(self, namespace: str, custom_logback_file_path: str):
        logback = open(custom_logback_file_path).read()
        data = {"logback-access.xml": logback}
        create_or_update_configmap(namespace, "logback-access-config", data)

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

    def test_daemon_pvc(self, ops_manager: MongoDBOpsManager, namespace: str):
        """Verifies the PVCs mounted to the pod"""
        for api_client, pod in ops_manager.read_backup_pods():
            claims = [volume for volume in pod.spec.volumes if getattr(volume, "persistent_volume_claim")]
            assert len(claims) == 1
            claims.sort(key=attrgetter("name"))
            pod_name = pod.metadata.name
            default_sc = get_default_storage_class()
            KubernetesTester.check_single_pvc(
                namespace,
                claims[0],
                "head",
                f"head-{pod_name}",
                "500M",
                default_sc,
                api_client=api_client,
            )

    def test_backup_daemon_services_created(self, ops_manager: MongoDBOpsManager, namespace: str):
        """Backup creates two additional services for queryable backup"""
        service_count = 0
        for api_client, _ in ops_manager.read_backup_pods():
            services = client.CoreV1Api(api_client=api_client).list_namespaced_service(namespace).items

            # If running locally in 'default' namespace, there might be more
            # services on it. Let's make sure we only count those that we care of.
            # For now we allow this test to fail, because it is too broad to be significant
            # and it is easy to break it.
            backup_services = [s for s in services if s.metadata.name.startswith("om-backup")]
            service_count += len(backup_services)

        assert service_count >= 3

    @skip_if_local
    def test_om(self, ops_manager: MongoDBOpsManager):
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        for pod_fqdn in ops_manager.backup_daemon_pods_headless_fqdns():
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
        for _, pod in ops_manager.read_appdb_pods():
            assert_pod_security_context(pod, managed)
            assert_pod_container_security_context(pod.spec.containers[0], managed)

    def test_security_contexts_om(
        self,
        ops_manager: MongoDBOpsManager,
        operator_installation_config: Dict[str, str],
    ):
        managed = operator_installation_config["managedSecurityContext"] == "true"
        for _, pod in ops_manager.read_om_pods():
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

        for pod_fqdn in ops_manager.backup_daemon_pods_headless_fqdns():
            om_tester.assert_daemon_enabled(pod_fqdn, HEAD_PATH)

        om_tester.assert_block_stores([new_om_data_store(blockstore_replica_set, "blockStore1")])
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
        om_tester.assert_s3_stores([new_om_s3_store(s3_replica_set, "s3Store1", s3_bucket, aws_s3_client)])

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
        for _, pod in pods:
            assert_pod_security_context(pod, managed)
            assert_pod_container_security_context(pod.spec.containers[0], managed)


@mark.e2e_om_ops_manager_backup
class TestOpsManagerWatchesBlockStoreUpdates:
    def test_om_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=40)

    def test_scramsha_enabled_for_blockstore(self, blockstore_replica_set: MongoDB):
        """Enables SCRAM for the blockstore replica set. Note that until CLOUDP-67736 is fixed
        the order of operations (scram first, MongoDBUser - next) is important"""
        blockstore_replica_set["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}
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
        ops_manager["spec"]["backup"]["blockStores"][0]["mongodbUserRef"] = {"name": blockstore_user.name}
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
        for pod_fqdn in ops_manager.backup_daemon_pods_headless_fqdns():
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
        om_tester.assert_s3_stores([new_om_s3_store(s3_replica_set, "s3Store1", s3_bucket, aws_s3_client)])


@mark.e2e_om_ops_manager_backup
class TestBackupForMongodb:
    """This part ensures that backup for the client works correctly and the snapshot is created.
    Both latest and the one before the latest are tested (as the backup process for them may differ significantly)"""

    @fixture(scope="class")
    def mdb_latest(self, ops_manager: MongoDBOpsManager, namespace, custom_mdb_version: str):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name="mdb-four-two",
        ).configure(ops_manager)
        resource.set_version(ensure_ent_version(custom_mdb_version))
        resource.configure_backup(mode="disabled")

        try_load(resource)
        return resource

    @fixture(scope="class")
    def mdb_prev(self, ops_manager: MongoDBOpsManager, namespace, custom_mdb_prev_version: str):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name="mdb-four-zero",
        ).configure(ops_manager)
        resource.set_version(ensure_ent_version(custom_mdb_prev_version))
        resource.configure_backup(mode="disabled")

        try_load(resource)
        return resource

    def test_mdbs_created(self, mdb_latest: MongoDB, mdb_prev: MongoDB):
        mdb_latest.update()
        mdb_prev.update()

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

    def test_can_transition_from_started_to_stopped(self, mdb_latest: MongoDB, mdb_prev: MongoDB):
        # a direction transition from enabled to disabled is a single
        # step for the operator
        mdb_prev.assert_backup_reaches_status("STARTED", timeout=100)
        mdb_prev.configure_backup(mode="disabled")
        mdb_prev.update()
        mdb_prev.assert_backup_reaches_status("STOPPED", timeout=600)

    def test_can_transition_from_started_to_terminated_0(self, mdb_latest: MongoDB, mdb_prev: MongoDB):
        # a direct transition from enabled to terminated is not possible
        # the operator should handle the transition from STARTED -> STOPPED -> TERMINATING
        mdb_latest.assert_backup_reaches_status("STARTED", timeout=100)
        mdb_latest.configure_backup(mode="terminated")
        mdb_latest.update()
        mdb_latest.assert_backup_reaches_status("TERMINATING", timeout=600)

    def test_backup_terminated_for_deleted_resource(self, ops_manager: MongoDBOpsManager, mdb_prev: MongoDB):
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

        # assert backup is deactivated
        om_tester_second.wait_until_backup_deactivated()

    def test_hosts_were_removed(self, ops_manager: MongoDBOpsManager, mdb_prev: MongoDB):
        om_tester_second = ops_manager.get_om_tester(project_name="secondProject")
        om_tester_second.wait_until_hosts_are_empty()

    def test_deploy_same_mdb_again_with_orphaned_backup(self, ops_manager: MongoDBOpsManager, mdb_prev: MongoDB):
        mdb_prev.configure_backup(mode="enabled")
        mdb_prev["spec"]["backup"]["autoTerminateOnDeletion"] = False
        # we need to make sure to use a clean resource since we might have loaded it
        del mdb_prev["metadata"]["resourceVersion"]
        mdb_prev.create()
        mdb_prev.assert_reaches_phase(Phase.Running)

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

        # assert backup is orphaned
        om_tester_second.wait_until_backup_running()

    def test_hosts_were_not_removed(self, ops_manager: MongoDBOpsManager, mdb_prev: MongoDB):
        om_tester_second = ops_manager.get_om_tester(project_name="secondProject")
        om_tester_second.wait_until_hosts_are_not_empty()


@mark.e2e_om_ops_manager_backup
class TestBackupSnapshotSchedule(BackupSnapshotScheduleTests):
    pass


@mark.e2e_om_ops_manager_backup
class TestAssignmentLabels:
    @fixture(scope="class")
    def project_name(self):
        return "mdb-assignment-labels"

    @fixture(scope="class")
    def mdb_assignment_labels(
        self,
        ops_manager: MongoDBOpsManager,
        namespace,
        project_name,
        custom_mdb_version: str,
    ):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name=project_name,
        ).configure(ops_manager)
        resource["spec"]["members"] = 1
        resource.set_version(ensure_ent_version(custom_mdb_version))
        resource["spec"]["backup"] = {}
        resource["spec"]["backup"]["assignmentLabels"] = ["test"]
        resource.configure_backup(mode="enabled")
        return resource.update()

    @fixture(scope="class")
    def mdb_assignment_labels_om_tester(self, ops_manager: MongoDBOpsManager, project_name: str) -> OMTester:
        ops_manager.load()
        return ops_manager.get_om_tester(project_name=project_name)

    def test_add_assignment_labels_to_the_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["backup"]["assignmentLabels"] = ["test"]
        ops_manager["spec"]["backup"]["blockStores"][0]["assignmentLabels"] = ["test"]
        ops_manager["spec"]["backup"]["opLogStores"][0]["assignmentLabels"] = ["test"]
        ops_manager["spec"]["backup"]["s3Stores"][0]["assignmentLabels"] = ["test"]

        ops_manager.update()
        ops_manager.om_status().assert_reaches_phase(Phase.Running, ignore_errors=True)

    def test_mdb_assignment_labels_created(self, mdb_assignment_labels: MongoDB):
        # In order to configure backup on the project level, the labels
        # on both Backup Daemons and project need to match. Otherwise, this will fail.
        mdb_assignment_labels.assert_reaches_phase(Phase.Running, ignore_errors=True)

    def test_assignment_labels_in_om(self, mdb_assignment_labels_om_tester: OMTester):
        # Those labels are set on the Ops Manager CR level
        mdb_assignment_labels_om_tester.api_read_backup_configs()
        mdb_assignment_labels_om_tester.assert_s3_stores([{"id": "s3Store1", "labels": ["test"]}])
        mdb_assignment_labels_om_tester.assert_oplog_stores([{"id": "oplog1", "labels": ["test"]}])
        mdb_assignment_labels_om_tester.assert_block_stores([{"id": "blockStore1", "labels": ["test"]}])
        # This one is set on the MongoDB CR level
        assert mdb_assignment_labels_om_tester.api_backup_group()["labelFilter"] == ["test"]


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
        om_tester.assert_s3_stores([new_om_s3_store(s3_replica_set, "s3Store1", s3_bucket, aws_s3_client)])

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
        om_tester.assert_s3_stores([new_om_s3_store(s3_replica_set, "s3Store1", s3_bucket, aws_s3_client)])
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
