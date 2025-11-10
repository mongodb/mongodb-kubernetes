import logging
import os
from operator import attrgetter
from typing import Dict, Optional

from kubernetes import client
from kubetester import (
    assert_pod_container_security_context,
    assert_pod_security_context,
    create_or_update_secret,
    get_default_storage_class,
)
from kubetester.awss3client import AwsS3Client, s3_endpoint
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import running_locally, skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.om_queryable_backups import generate_queryable_pem
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.constants import AWS_REGION
from tests.opsmanager.om_ops_manager_backup import create_aws_secret, create_s3_bucket
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

TEST_DB = "testdb"
TEST_COLLECTION = "testcollection"
TEST_DATA = {"_id": "unique_id", "name": "John", "address": "Highway 37", "age": 30}

PROJECT_NAME = "firstProject"

LOGLEVEL = os.environ.get("LOGLEVEL", "DEBUG").upper()
logging.basicConfig(level=LOGLEVEL)

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


@fixture(scope="module")
def queryable_pem_secret(namespace: str) -> str:
    return create_or_update_secret(
        namespace,
        "queryable-bkp-pem",
        {"queryable.pem": generate_queryable_pem(namespace)},
    )


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


@fixture(scope="module")
def ops_manager(
    namespace: str,
    s3_bucket: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    queryable_pem_secret: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup.yaml"), namespace=namespace
    )

    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket
    resource["spec"]["backup"]["headDB"]["storageClass"] = get_default_storage_class()
    resource["spec"]["backup"]["members"] = 1
    resource["spec"]["backup"]["queryableBackupSecretRef"] = {"name": queryable_pem_secret}

    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()

    resource["spec"]["configuration"]["mongodb.release.autoDownload.enterprise"] = "true"

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    resource.update()
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

    resource.update()
    return resource


@fixture(scope="module")
def s3_replica_set(ops_manager, namespace) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=S3_RS_NAME,
    ).configure(ops_manager, "s3metadata")

    resource.update()
    return resource


@fixture(scope="module")
def blockstore_replica_set(ops_manager, namespace, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=BLOCKSTORE_RS_NAME,
    ).configure(ops_manager, "blockstore")

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


@fixture(scope="module")
def mdb42(ops_manager: MongoDBOpsManager, namespace, custom_mdb_version: str):
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name="mdb-four-two",
    ).configure(ops_manager, PROJECT_NAME)
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.configure_backup(mode="enabled")
    resource.update()
    return resource


@fixture(scope="module")
def mdb_test_collection(mdb42: MongoDB):
    dbClient = mdb42.tester().client[TEST_DB]
    return dbClient[TEST_COLLECTION]


@mark.e2e_om_ops_manager_queryable_backup
class TestOpsManagerCreation:
    """
    name: Ops Manager successful creation with backup and oplog stores enabled
    description: |
      Creates an Ops Manager instance with backup enabled. The OM is expected to get to 'Pending' state
      eventually as it will wait for oplog db to be created
    """

    def test_create_om(self, ops_manager: MongoDBOpsManager):
        """creates a s3 bucket, s3 config and an OM resource (waits until Backup gets to Pending state)"""
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            msg_regexp="The MongoDB object .+ doesn't exist",
            timeout=1200,
        )

    def test_daemon_statefulset(self, ops_manager: MongoDBOpsManager):
        def stateful_set_becomes_ready():
            stateful_set = ops_manager.read_backup_statefulset()
            return stateful_set.status.ready_replicas == 1 and stateful_set.status.current_replicas == 1

        KubernetesTester.wait_until(stateful_set_becomes_ready, timeout=300)

        stateful_set = ops_manager.read_backup_statefulset()
        # pod template has volume mount request
        assert (HEAD_PATH, "head") in (
            (mount.mount_path, mount.name) for mount in stateful_set.spec.template.spec.containers[0].volume_mounts
        )

    def test_daemon_pvc(self, ops_manager: MongoDBOpsManager, namespace: str):
        """Verifies the PVCs mounted to the pod"""
        pods = ops_manager.read_backup_pods()
        idx = 0
        for api_client, pod in pods:
            claims = [volume for volume in pod.spec.volumes if getattr(volume, "persistent_volume_claim")]
            assert len(claims) == 1
            claims.sort(key=attrgetter("name"))

            default_sc = get_default_storage_class()
            KubernetesTester.check_single_pvc(
                namespace,
                claims[0],
                "head",
                "head-{}-{}".format(ops_manager.backup_daemon_sts_name(), idx),
                "500M",
                default_sc,
                api_client=api_client,
            )
            idx += 1

    def test_backup_daemon_services_created(self, namespace):
        """Backup creates two additional services for queryable backup"""
        services = client.CoreV1Api().list_namespaced_service(namespace).items

        # If running locally in 'default' namespace, there might be more
        # services on it. Let's make sure we only count those that we care of.
        # For now we allow this test to fail, because it is too broad to be significant
        # and it is easy to break it.
        backup_services = [s for s in services if s.metadata.name.startswith("om-backup")]

        assert len(backup_services) >= 3

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
        for api_client, pod in ops_manager.read_om_pods():
            assert_pod_security_context(pod, managed)
            assert_pod_container_security_context(pod.spec.containers[0], managed)


@mark.e2e_om_ops_manager_queryable_backup
class TestBackupDatabasesAdded:
    """name: Creates three mongodb resources for oplog, s3 and blockstore and waits until OM resource gets to
    running state"""

    def test_backup_mdbs_created(
        self,
        oplog_replica_set: MongoDB,
        s3_replica_set: MongoDB,
        blockstore_replica_set: MongoDB,
        oplog_user: MongoDBUser,
    ):
        """Creates mongodb databases all at once"""
        oplog_replica_set.load()
        oplog_replica_set["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}
        oplog_replica_set.update()
        oplog_replica_set.assert_reaches_phase(Phase.Running)
        s3_replica_set.assert_reaches_phase(Phase.Running)
        blockstore_replica_set.assert_reaches_phase(Phase.Running)
        oplog_user.assert_reaches_phase(Phase.Updated)

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


@mark.e2e_om_ops_manager_queryable_backup
class TestOpsManagerWatchesBlockStoreUpdates:
    def test_om_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=40)

    def test_scramsha_enabled_for_blockstore(self, blockstore_replica_set: MongoDB, blockstore_user: MongoDBUser):
        """Enables SCRAM for the blockstore replica set. Note that until CLOUDP-67736 is fixed
        the order of operations (scram first, MongoDBUser - next) is important"""
        blockstore_replica_set["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}
        blockstore_replica_set.update()

        # timeout of 600 is required when enabling SCRAM in mdb5.0.0
        blockstore_replica_set.assert_reaches_phase(Phase.Running, timeout=900)
        blockstore_user.assert_reaches_phase(Phase.Updated)

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


@mark.e2e_om_ops_manager_queryable_backup
class TestBackupForMongodb:
    """This part ensures that backup for the client works correctly and the snapshot is created.
    Both latest and the one before the latest are tested (as the backup process for them may differ significantly)"""

    def test_mdbs_created(self, mdb42: MongoDB):
        mdb42.assert_reaches_phase(Phase.Running)

    def test_add_test_data(self, mdb_test_collection):
        mdb_test_collection.insert_one(TEST_DATA)

    def test_mdbs_backuped(self, ops_manager: MongoDBOpsManager):
        om_tester = ops_manager.get_om_tester(project_name=PROJECT_NAME)
        # wait until a first snapshot is ready
        om_tester.wait_until_backup_snapshots_are_ready(expected_count=1)


@mark.e2e_om_ops_manager_queryable_backup
class TestQueryableBackup:
    """This part queryable backup is enabled and we can query the first snapshot."""

    def test_queryable_backup(self, ops_manager: MongoDBOpsManager):
        CONNECTION_TIMEOUT = 600
        om_tester = ops_manager.get_om_tester(project_name=PROJECT_NAME)
        records = om_tester.query_backup(TEST_DB, TEST_COLLECTION, CONNECTION_TIMEOUT)
        assert len(records) > 0
