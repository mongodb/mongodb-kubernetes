from operator import attrgetter
from typing import Optional, Dict

from kubernetes import client
from kubetester import MongoDB, MongoDBOpsManager
from kubetester.awss3client import AwsS3Client, s3_endpoint
from kubetester.kubetester import (
    skip_if_local,
    fixture as yaml_fixture,
    KubernetesTester,
)
from kubetester.mongodb import Phase
from pytest import mark, fixture
from tests.opsmanager.om_base import OpsManagerBase

HEAD_PATH = "/head/"
S3_SECRET_NAME = "my-s3-secret"
AWS_REGION = "us-east-1"
OPLOG_RS_NAME = "my-mongodb-oplog"
S3_RS_NAME = "my-mongodb-s3"

"""
Current test focuses on backup capabilities
Note the strategy for Ops Manager testing: the tests should have more than 1 updates - this is because the initial
creation of Ops Manager takes too long, so we try to avoid fine-grained test cases and combine different
updates in one test.
"""


# TODO: improve this function to be generic and put on MongoDB resource object
def mongo_uri(name: str, namespace: Optional[str] = None) -> str:
    if namespace is None:
        namespace = KubernetesTester.get_namespace()
    return (
        f"mongodb://{name}-0.{name}-svc.{namespace}.svc.cluster.local:27017,"
        f"{name}-1.{name}-svc.{namespace}.svc.cluster.local:27017,"
        f"{name}-2.{name}-svc.{namespace}.svc.cluster.local:27017/?"
        f"connectTimeoutMS=20000&replicaSet={name}&serverSelectionTimeoutMS=20000"
    )


def new_om_s3_store(
    s3_id: str,
    s3_bucket_name: str,
    aws_s3_client: AwsS3Client,
    assignment_enabled: bool = True,
    path_style_access_enabled: bool = True,
    rs_name: str = S3_RS_NAME,
) -> Dict:
    return {
        "uri": mongo_uri(rs_name),
        "id": s3_id,
        "pathStyleAccessEnabled": path_style_access_enabled,
        "s3BucketEndpoint": s3_endpoint(AWS_REGION),
        "s3BucketName": s3_bucket_name,
        "awsAccessKey": aws_s3_client.aws_access_key,
        "awsSecretKey": aws_s3_client.aws_secret_access_key,
        "assignmentEnabled": assignment_enabled,
    }


def new_om_oplog_store(
    oplog_store_name: str,
    ssl: bool = False,
    assignment_enabled: bool = True,
    oplog_rs_name=OPLOG_RS_NAME,
) -> Dict:
    return {
        "id": oplog_store_name,
        "uri": mongo_uri(oplog_rs_name),
        "ssl": ssl,
        "assignmentEnabled": assignment_enabled,
    }


@fixture(scope="module")
def s3_bucket(aws_s3_client: AwsS3Client) -> str:
    """ creates a s3 bucket and a s3 config"""

    bucket_name = KubernetesTester.random_k8s_name("test-bucket-")
    aws_s3_client.create_s3_bucket(bucket_name)
    print(f"\nCreated S3 bucket {bucket_name}")

    OpsManagerBase.create_secret(
        OpsManagerBase.get_namespace(),
        S3_SECRET_NAME,
        {
            "accessKey": aws_s3_client.aws_access_key,
            "secretKey": aws_s3_client.aws_secret_access_key,
        },
    )
    print(f"Created a secret for S3 credentials {S3_SECRET_NAME}")
    yield bucket_name

    print(f"\nRemoving S3 bucket {bucket_name}")
    aws_s3_client.delete_s3_bucket(bucket_name)


@fixture(scope="module")
def ops_manager(namespace, s3_bucket) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup.yaml"), namespace=namespace
    )

    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket

    yield resource.create()


@fixture(scope="module")
def s3_bucket_2(aws_s3_client: AwsS3Client) -> str:
    """ creates a s3 bucket and a s3 config"""

    bucket_name = KubernetesTester.random_k8s_name("test-bucket-")
    aws_s3_client.create_s3_bucket(bucket_name)
    print(f"\nCreated S3 bucket {bucket_name}")

    yield bucket_name

    print(f"\nRemoving S3 bucket {bucket_name}")
    aws_s3_client.delete_s3_bucket(bucket_name)


@mark.e2e_om_ops_manager_backup
class TestOpsManagerCreation:
    """
    name: Ops Manager successful creation with backup and oplog stores enabled
    description: |
      Creates an Ops Manager instance with backup enabled. The OM is expected to get to 'Pending' state
      eventually as it will wait for oplog db to be created
    """

    def test_setup_gp2_storage_class(self):
        KubernetesTester.make_default_gp2_storage_class()

    def test_create_om(self, ops_manager):
        """ creates a s3 bucket, s3 config and an OM resource (waits until gets to Pending state)"""
        ops_manager.assert_reaches_phase(
            Phase.Pending,
            msg_regexp="The MongoDB object .+ doesn't exist",
            timeout=900,
        )

    def test_daemon_statefulset(self, ops_manager: MongoDBOpsManager):
        statefulset = ops_manager.get_backup_statefulset()
        assert statefulset.status.ready_replicas == 1
        assert statefulset.status.current_replicas == 1

        # pod template has volume mount request
        assert (HEAD_PATH, "head") in (
            (mount.mount_path, mount.name)
            for mount in statefulset.spec.template.spec.containers[0].volume_mounts
        )

    def test_daemon_pvc(self, ops_manager: MongoDBOpsManager, namespace: str):
        """ Verifies the PVCs mounted to the pod """
        pod = ops_manager.get_backup_pod()
        claims = [
            volume
            for volume in pod.spec.volumes
            if getattr(volume, "persistent_volume_claim")
        ]
        assert len(claims) == 1
        claims.sort(key=attrgetter("name"))

        KubernetesTester.check_single_pvc(
            namespace,
            claims[0],
            "head",
            "head-{}-0".format(ops_manager.backup_daemon_name()),
            "500M",
            "gp2",
        )

    def test_no_daemon_service_created(self, namespace):
        """ Backup daemon serves no incoming traffic so no service must be created """
        services = client.CoreV1Api().list_namespaced_service(namespace).items

        # 1 for AppDB, 2 for Ops Manager statefulset, 1 for validation webhook
        assert len(services) == 4

    @skip_if_local
    def test_om(self, ops_manager: MongoDBOpsManager):
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        om_tester.assert_daemon_enabled(ops_manager.backup_daemon_pod_name(), HEAD_PATH)
        # No oplog stores were created in Ops Manager by this time
        om_tester.assert_oplog_stores([])
        om_tester.assert_s3_stores([])


@mark.e2e_om_ops_manager_backup
class TestBackupDatabasesAdded:
    """ name: Creates two mongodb resources for oplog and s3 and waits until OM resource gets to running state"""

    @fixture(scope="class")
    def oplog_replica_set(self, ops_manager, namespace):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name=OPLOG_RS_NAME,
        ).configure(ops_manager, "development")
        resource["spec"]["version"] = "3.6.10"

        yield resource.create()

    @fixture(scope="class")
    def s3_replica_set(self, ops_manager, namespace):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name=S3_RS_NAME,
        ).configure(ops_manager, "s3metadata")
        resource["spec"]["version"] = "3.6.10"

        yield resource.create()

    def test_oplog_replica_set_created(self, oplog_replica_set: MongoDB):
        oplog_replica_set.assert_reaches_phase(Phase.Running)

    def test_s3_replica_set_created(self, s3_replica_set: MongoDB):
        s3_replica_set.assert_reaches_phase(Phase.Running)

    @skip_if_local
    def test_om(
        self, s3_bucket: str, aws_s3_client: AwsS3Client, ops_manager: MongoDBOpsManager
    ):
        """ As soon as oplog mongodb store is created Operator will create oplog configs in OM and
        get to Running state. Note, that the OM may quickly get into error state (BACKUP_MONGO_CONNECTION_FAILED)
        as the backup replica sets may not be completely ready but this will get fixed soon after a retry. """
        ops_manager.assert_reaches_phase(Phase.Running, timeout=200, ignore_errors=True)

        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        # Nothing has changed for daemon
        om_tester.assert_daemon_enabled(ops_manager.backup_daemon_pod_name(), HEAD_PATH)

        om_tester.assert_oplog_stores([new_om_oplog_store("oplog1")])
        om_tester.assert_s3_stores(
            [new_om_s3_store("s3Store1", s3_bucket, aws_s3_client)]
        )


@mark.e2e_om_ops_manager_backup
class TestBackupForMongodb:
    """ This part ensures that backup for the client works correctly and the snapshot is created.
    Both Mdb 4.0 and 4.2 are tested (as the backup process for them differs significantly) """

    @fixture(scope="class")
    def mdb_4_2(self, ops_manager: MongoDBOpsManager, namespace):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name="mdb-four-two",
        ).configure(ops_manager, "firstProject")
        # MongoD versions greater than 4.2.0 must be enterprise build to enable backup
        resource["spec"]["version"] = "4.2.2-ent"

        return resource.create()

    @fixture(scope="class")
    def mdb_4_0(self, ops_manager: MongoDBOpsManager, namespace):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name="mdb-four-zero",
        ).configure(ops_manager, "secondProject")
        resource["spec"]["version"] = "4.0.16"

        return resource.create()

    def test_mdbs_created(self, mdb_4_2: MongoDB, mdb_4_0: MongoDB):
        mdb_4_2.assert_reaches_phase(Phase.Running)
        mdb_4_0.assert_reaches_phase(Phase.Running)

    def test_mdbs_backuped(self, ops_manager: MongoDBOpsManager):
        om_tester_first = ops_manager.get_om_tester(project_name="firstProject")
        om_tester_first.enable_backup()

        om_tester_second = ops_manager.get_om_tester(project_name="secondProject")
        om_tester_second.enable_backup()

        # wait until a first snapshot is ready for both
        om_tester_first.wait_until_backup_snapshots_are_ready(expected_count=1)
        om_tester_second.wait_until_backup_snapshots_are_ready(expected_count=1)


@mark.e2e_om_ops_manager_backup
class TestBackupConfigurationAdditionDeletion:
    def test_oplog_store_is_added(
        self, ops_manager: MongoDBOpsManager, s3_bucket: str, aws_s3_client: AwsS3Client
    ):
        ops_manager.reload()
        ops_manager["spec"]["backup"]["oplogStores"].append(
            {"name": "oplog2", "mongodbResourceRef": {"name": S3_RS_NAME}}
        )

        ops_manager.update()
        ops_manager.assert_reaches_phase(Phase.Reconciling, timeout=60)
        ops_manager.assert_reaches_phase(Phase.Running, timeout=600)

        om_tester = ops_manager.get_om_tester()
        om_tester.assert_oplog_stores(
            [
                new_om_oplog_store("oplog1"),
                new_om_oplog_store("oplog2", oplog_rs_name=S3_RS_NAME),
            ]
        )
        om_tester.assert_s3_stores(
            [new_om_s3_store("s3Store1", s3_bucket, aws_s3_client)]
        )

    def test_s3_store_is_updated(
        self,
        ops_manager: MongoDBOpsManager,
        s3_bucket_2: str,
        aws_s3_client: AwsS3Client,
    ):
        ops_manager.reload()
        existing_s3_store = ops_manager["spec"]["backup"]["s3Stores"][0]
        existing_s3_store["s3BucketName"] = s3_bucket_2

        ops_manager.update()
        ops_manager.assert_reaches_phase(Phase.Reconciling, timeout=60)
        ops_manager.assert_reaches_phase(Phase.Running, timeout=600)

        om_tester = ops_manager.get_om_tester()

        om_tester.assert_oplog_stores(
            [
                new_om_oplog_store("oplog1"),
                new_om_oplog_store("oplog2", oplog_rs_name=S3_RS_NAME),
            ]
        )
        om_tester.assert_s3_stores(
            [new_om_s3_store("s3Store1", s3_bucket_2, aws_s3_client),]
        )

    def test_oplog_store_is_deleted_correctly(
        self,
        ops_manager: MongoDBOpsManager,
        s3_bucket_2: str,
        aws_s3_client: AwsS3Client,
    ):
        ops_manager.reload()
        ops_manager["spec"]["backup"]["oplogStores"].pop()
        ops_manager.update()

        ops_manager.assert_reaches_phase(Phase.Reconciling, timeout=60)
        ops_manager.assert_reaches_phase(Phase.Running, timeout=600)

        om_tester = ops_manager.get_om_tester()

        om_tester.assert_oplog_stores([new_om_oplog_store("oplog1")])
        om_tester.assert_s3_stores(
            [new_om_s3_store("s3Store1", s3_bucket_2, aws_s3_client),]
        )

    def test_error_on_s3store_removal(
        self, ops_manager: MongoDBOpsManager,
    ):
        """ Removing the s3 store when there are backups running is an error """
        ops_manager.reload()
        ops_manager["spec"]["backup"]["s3Stores"] = []
        ops_manager.update()

        ops_manager.assert_reaches_phase(Phase.Reconciling, timeout=60)
        ops_manager.assert_reaches_phase(
            Phase.Failed,
            msg_regexp=".*BACKUP_CANNOT_REMOVE_S3_STORE_CONFIG.*",
            timeout=150,
        )
