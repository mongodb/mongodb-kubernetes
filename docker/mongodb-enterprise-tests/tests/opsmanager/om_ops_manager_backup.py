from operator import attrgetter

import yaml
from kubetester import MongoDB
from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import (
    skip_if_local,
    fixture as yaml_fixture,
    KubernetesTester,
)
from kubetester.omtester import OMTester
from pytest import mark, fixture
from tests.opsmanager.om_base import OpsManagerBase

HEAD_PATH = "/head/"

"""
Current test focuses on backup capabilities
Note the strategy for Ops Manager testing: the tests should have more than 1 updates - this is because the initial
creation of Ops Manager takes too long, so we try to avoid fine-grained test cases and combine different
updates in one test.
"""


@mark.e2e_om_ops_manager_backup
class TestOpsManagerCreation(OpsManagerBase):
    """
    name: Ops Manager successful creation with backup and oplog stores enabled
    description: |
      Creates an Ops Manager instance with backup enabled. The OM is expected to get to 'Pending' state
      eventually as it will wait for oplog db to be created
    """

    @fixture(scope="module")
    def s3_bucket(self):
        """ creates an s3 bucket and an s3 config"""
        aws_helper = AwsS3Client("us-east-1")

        bucket_name = KubernetesTester.random_k8s_name("test-bucket-")
        aws_helper.create_s3_bucket(bucket_name)
        print(f"\nCreated S3 bucket {bucket_name}")

        s3_secret = "my-s3-secret"
        self.create_secret(
            KubernetesTester.get_namespace(),
            s3_secret,
            {
                "accessKey": aws_helper.aws_access_key,
                "secretKey": aws_helper.aws_secret_access_key,
            },
        )
        print(f"Created a secret for S3 credentials {s3_secret}")
        yield bucket_name

        print(f"\nRemoving S3 bucket {bucket_name}")

        aws_helper.delete_s3_bucket(bucket_name)

    def test_create_om(self, s3_bucket):
        """ creates a s3 bucket, s3 config and an OM resource (waits until gets to Pending state)"""
        resource = yaml.safe_load(open(yaml_fixture("om_ops_manager_backup.yaml")))

        patch = [
            {
                "op": "replace",
                "path": "/spec/backup/s3Stores/0/s3BucketName",
                "value": s3_bucket,
            }
        ]
        self.create_custom_resource_from_object(
            self.get_namespace(), resource, patch=patch
        )

        self.wait_until("om_in_pending_state_mongodb_doesnt_exist", 900)

        # TODO this one is super ugly but we need this to initialize an instance variable 'om_cr'...
        super().setup_class()

    def test_daemon_statefulset(self):
        statefulset = self.appsv1.read_namespaced_stateful_set_status(
            self.om_cr.backup_sts_name(), self.namespace
        )
        assert statefulset.status.ready_replicas == 1
        assert statefulset.status.current_replicas == 1

        # pod template has volume mount request
        assert (HEAD_PATH, "head") in (
            (mount.mount_path, mount.name)
            for mount in statefulset.spec.template.spec.containers[0].volume_mounts
        )

    def test_daemon_pvc(self):
        """ Verifies the PVCs mounted to the pod """
        pod = self.corev1.read_namespaced_pod(
            self.om_cr.backup_pod_name(), self.namespace
        )
        claims = [
            volume
            for volume in pod.spec.volumes
            if getattr(volume, "persistent_volume_claim")
        ]
        assert len(claims) == 1
        claims.sort(key=attrgetter("name"))

        self.check_single_pvc(
            claims[0], "head", self.om_cr.backup_head_pvc_name(), "500M", "gp2"
        )

    def test_no_daemon_service_created(self):
        """ Backup daemon serves no incoming traffic so no service must be created """
        services = self.corev1.list_namespaced_service(self.namespace).items

        # 1 for AppDB and 1 for Ops Manager statefulset
        assert len(services) == 2

    @skip_if_local
    def test_om(self):
        om_tester = OMTester(self.om_context)
        om_tester.assert_healthiness()
        om_tester.assert_daemon_enabled(self.om_cr.backup_pod_name(), HEAD_PATH)
        # No oplog stores were created in Ops Manager by this time
        om_tester.assert_oplog_stores([])


@mark.e2e_om_ops_manager_backup
class TestBackupDatabasesAdded(OpsManagerBase):
    """ name: Creates two mongodb resources for oplog and s3 and waits until OM resource gets to running state"""

    @fixture(scope="class")
    def oplog_replica_set(self, namespace):
        self.create_configmap(
            self.namespace,
            "oplog-mongodb-config-map",
            {"baseUrl": self.om_cr.get_om_status_url(), "projectName": "development"},
        )
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name="my-mongodb-oplog",
        )

        resource["spec"]["version"] = "3.6.10"
        resource["spec"]["opsManager"]["configMapRef"][
            "name"
        ] = "oplog-mongodb-config-map"
        resource["spec"]["credentials"] = self.om_cr.api_key_secret()

        yield resource.create()

    @fixture(scope="class")
    def s3_replica_set(self, namespace):
        self.create_configmap(
            self.namespace,
            "s3-mongodb-config-map",
            {"baseUrl": self.om_cr.get_om_status_url(), "projectName": "s3metadata"},
        )
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set-for-om.yaml"),
            namespace=namespace,
            name="my-mongodb-s3",
        )

        resource["spec"]["version"] = "3.6.10"
        resource["spec"]["opsManager"]["configMapRef"]["name"] = "s3-mongodb-config-map"
        resource["spec"]["credentials"] = self.om_cr.api_key_secret()

        yield resource.create()

    def test_oplog_replica_set_created(self, oplog_replica_set: MongoDB):
        oplog_replica_set.assert_reaches_phase("Running")

        assert oplog_replica_set["status"]["phase"] == "Running"

    def test_s3_replica_set_created(self, s3_replica_set: MongoDB):
        s3_replica_set.assert_reaches_phase("Running")

        assert s3_replica_set["status"]["phase"] == "Running"

    @skip_if_local
    def test_om(self, namespace):
        """ As soon as oplog mongodb store is created Operator will create oplog configs in OM and
        get to Running state"""
        self.wait_until("om_in_running_state", timeout=200)
        om_tester = OMTester(self.om_context)
        om_tester.assert_healthiness()
        # Nothing has changed for daemon
        om_tester.assert_daemon_enabled(self.om_cr.backup_pod_name(), HEAD_PATH)

        # One oplog store was created
        # TODO this is a good candidate for mongo_uri() function to 'MongoDB' object
        mongo_uri = (
            f"mongodb://my-mongodb-oplog-0.my-mongodb-oplog-svc.{namespace}.svc.cluster.local:27017,"
            f"my-mongodb-oplog-1.my-mongodb-oplog-svc.{namespace}.svc.cluster.local:27017,"
            f"my-mongodb-oplog-2.my-mongodb-oplog-svc.{namespace}.svc.cluster.local:27017/?"
            f"connectTimeoutMS=20000&replicaSet=my-mongodb-oplog&serverSelectionTimeoutMS=20000"
        )
        om_tester.assert_oplog_stores(
            [
                {
                    "id": "oplog1",
                    "uri": mongo_uri,
                    "ssl": False,
                    "assignmentEnabled": True,
                }
            ]
        )
