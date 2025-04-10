"""

This test is meant to be run manually.

This is a companion test for docs/investigation/pod-is-killed-while-agent-restores.md

"""

from typing import Dict, Optional

from kubetester import create_or_update_secret, get_default_storage_class
from kubetester.awss3client import AwsS3Client, s3_endpoint
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB, Phase
from kubetester.mongodb_user import MongoDBUser
from kubetester.opsmanager import MongoDBOpsManager
from pytest import fixture, mark
from tests.conftest import is_multi_cluster
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

HEAD_PATH = "/head/"
S3_SECRET_NAME = "my-s3-secret"
OPLOG_RS_NAME = "my-mongodb-oplog"
S3_RS_NAME = "my-mongodb-s3"
BLOCKSTORE_RS_NAME = "my-mongodb-blockstore"
USER_PASSWORD = "/qwerty@!#:"


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
    return create_s3_bucket(aws_s3_client, bucket_prefix="test-s3-bucket-")


def create_aws_secret(aws_s3_client, secret_name: str, namespace: str):
    create_or_update_secret(
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
    bucket_prefix = KubernetesTester.random_k8s_name(bucket_prefix)
    aws_s3_client.create_s3_bucket(bucket_prefix)
    print("Created S3 bucket", bucket_prefix)

    return bucket_prefix


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

    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket
    resource["spec"]["backup"]["headDB"]["storageClass"] = get_default_storage_class()
    resource["spec"]["externalConnectivity"] = {"type": "LoadBalancer"}

    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

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

    resource["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}

    return resource.create()


@fixture(scope="module")
def s3_replica_set(ops_manager, namespace) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=S3_RS_NAME,
    ).configure(ops_manager, "s3metadata")

    return resource.create()


@fixture(scope="module")
def blockstore_replica_set(ops_manager, namespace, custom_mdb_version: str) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=BLOCKSTORE_RS_NAME,
    ).configure(ops_manager, "blockstore")
    resource.set_version(custom_mdb_version)
    return resource.create()


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

    yield resource.create()


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

    yield resource.create()


@mark.e2e_om_ops_manager_backup_manual
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
            timeout=900,
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

    def test_om(self, ops_manager: MongoDBOpsManager):
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        for pod_fqdn in ops_manager.backup_daemon_pods_headless_fqdns():
            om_tester.assert_daemon_enabled(pod_fqdn, HEAD_PATH)

        # No oplog stores were created in Ops Manager by this time
        om_tester.assert_oplog_stores([])
        om_tester.assert_s3_stores([])


@mark.e2e_om_ops_manager_backup_manual
class TestBackupDatabasesAdded:
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

    def test_fix_om(self, ops_manager: MongoDBOpsManager, oplog_user: MongoDBUser):
        ops_manager.load()
        ops_manager["spec"]["backup"]["opLogStores"][0]["mongodbUserRef"] = {"name": oplog_user.name}
        ops_manager.update()

        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running,
            timeout=200,
            ignore_errors=True,
        )


@mark.e2e_om_ops_manager_backup_manual
class TestOpsManagerWatchesBlockStoreUpdates:
    def test_om_running(self, ops_manager: MongoDBOpsManager):
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, timeout=40)

    def test_scramsha_enabled_for_blockstore(self, blockstore_replica_set: MongoDB):
        """Enables SCRAM for the blockstore replica set. Note that until CLOUDP-67736 is fixed
        the order of operations (scram first, MongoDBUser - next) is important"""
        blockstore_replica_set["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}
        blockstore_replica_set.update()
        blockstore_replica_set.assert_reaches_phase(Phase.Running)

    def test_blockstore_user_was_added_to_om(self, blockstore_user: MongoDBUser):
        blockstore_user.assert_reaches_phase(Phase.Updated)

    def test_configure_blockstore_user(self, ops_manager: MongoDBOpsManager, blockstore_user: MongoDBUser):
        ops_manager.reload()
        ops_manager["spec"]["backup"]["blockStores"][0]["mongodbUserRef"] = {"name": blockstore_user.name}
        ops_manager.update()
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running,
            timeout=200,
            ignore_errors=True,
        )
        assert ops_manager.backup_status().get_message() is None


@mark.e2e_om_ops_manager_backup_manual
class TestBackupForMongodb:
    @fixture(scope="class")
    def mdb_latest(self, ops_manager: MongoDBOpsManager, namespace, custom_mdb_version: str):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name="rs-fixed",
        ).configure(ops_manager, "firstProject")
        resource.set_version(ensure_ent_version(custom_mdb_version))

        resource["spec"]["podSpec"] = {"podTemplate": {"spec": {}}}
        resource["spec"]["podSpec"]["podTemplate"]["spec"]["containers"] = [
            {
                "name": "mongodb-enterprise-database",
                "resources": {
                    "requests": {
                        "memory": "6Gi",
                        "cpu": "1",
                    }
                },
            }
        ]
        resource["spec"]["podSpec"]["podTemplate"]["spec"]["initContainers"] = [
            {
                "name": "mongodb-enterprise-init-database",
                "image": "268558157000.dkr.ecr.us-east-1.amazonaws.com/rodrigo/ubuntu/mongodb-enterprise-init-database:latest-fixed-probe",
            }
        ]

        resource.configure_backup(mode="disabled")
        return resource.create()

    @fixture(scope="class")
    def mdb_non_fixed(self, ops_manager: MongoDBOpsManager, namespace, custom_mdb_version: str):
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name="rs-not-fixed",
        ).configure(ops_manager, "secondProject")
        resource.set_version(ensure_ent_version(custom_mdb_version))

        resource["spec"]["podSpec"] = {"podTemplate": {"spec": {}}}
        resource["spec"]["podSpec"]["podTemplate"]["spec"]["containers"] = [
            {
                "name": "mongodb-enterprise-database",
                "resources": {
                    "requests": {
                        "memory": "6Gi",
                        "cpu": "1",
                    }
                },
            }
        ]

        resource["spec"]["podSpec"]["podTemplate"]["spec"]["initContainers"] = [
            {
                "name": "mongodb-enterprise-init-database",
                "image": "268558157000.dkr.ecr.us-east-1.amazonaws.com/rodrigo/ubuntu/mongodb-enterprise-init-database:non-fixed-probe",
            }
        ]

        resource.configure_backup(mode="disabled")
        return resource.create()

    def test_mdbs_created(self, mdb_latest: MongoDB, mdb_non_fixed: MongoDB):
        mdb_latest.assert_reaches_phase(Phase.Running)
        mdb_non_fixed.assert_reaches_phase(Phase.Running)

    def test_mdbs_enable_backup(self, mdb_latest: MongoDB, mdb_non_fixed: MongoDB):
        mdb_latest.load()
        mdb_latest.configure_backup(mode="enabled")
        mdb_latest.update()
        mdb_latest.assert_reaches_phase(Phase.Running)

        mdb_non_fixed.load()
        mdb_non_fixed.configure_backup(mode="enabled")
        mdb_non_fixed.update()
        mdb_non_fixed.assert_reaches_phase(Phase.Running)
