from operator import attrgetter

from pytest import mark, fixture
from kubetester import MongoDB
from kubetester.kubetester import skip_if_local, fixture as yaml_fixture
from kubetester.omtester import OMTester
from tests.opsmanager.om_base import OpsManagerBase

HEAD_PATH = "/head"

"""
Current test focuses on backup capabilities
Note the strategy for Ops Manager testing: the tests should have more than 1 updates - this is because the initial
creation of Ops Manager takes too long, so we try to avoid fine-grained test cases and combine different
updates in one test 
"""


@mark.e2e_om_ops_manager_backup
class TestOpsManagerCreation(OpsManagerBase):
    """
    name: Ops Manager successful creation with backup and oplog stores enabled
    description: |
      Creates an Ops Manager instance with backup enabled. The OM is expected to get to 'Pending' state
      eventually as it will wait for oplog db to be created
    create:
      file: om_ops_manager_backup.yaml
      wait_until: om_in_pending_state_mongodb_doesnt_exist
      timeout: 900
    """

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

        # 1 for AppDB and 2 for Ops Manager statefulset
        assert len(services) == 3

    @skip_if_local
    def test_om(self):
        om_tester = OMTester(self.om_context)
        om_tester.assert_healthiness()
        om_tester.assert_daemon_enabled(self.om_cr.backup_pod_name(), HEAD_PATH)
        # No oplog stores were created in Ops Manager by this time
        om_tester.assert_oplog_stores([])


@mark.e2e_om_ops_manager_backup
class TestOplogAdded(OpsManagerBase):
    @fixture(scope="class")
    def oplog_replica_set(self, namespace):
        self.create_configmap(
            self.namespace,
            "oplog-mongodb-config-map",
            {"baseUrl": self.om_cr.get_om_status_url(), "projectName": "development"},
        )
        resource = MongoDB.from_yaml(
            yaml_fixture("replica-set.yaml"),
            namespace=namespace,
            name="my-mongodb-oplog",
        )

        resource["spec"]["version"] = "3.6.10"
        resource["spec"]["opsManager"]["configMapRef"][
            "name"
        ] = "oplog-mongodb-config-map"
        resource["spec"]["credentials"] = self.om_cr.api_key_secret()

        yield resource.create()

    def test_oplog_replica_set_created(self, oplog_replica_set: MongoDB):
        oplog_replica_set.reaches_phase("Running")

        assert oplog_replica_set["status"]["phase"] == "Running"

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
