from typing import Optional

from kubetester import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.awss3client import AwsS3Client
from kubetester.kubetester import (
    skip_if_local,
    fixture as yaml_fixture,
    KubernetesTester,
)
from kubernetes import client
from kubetester.mongodb import Phase
from pytest import mark, fixture
from tests.opsmanager.om_ops_manager_backup import (
    HEAD_PATH,
    OPLOG_RS_NAME,
    new_om_data_store,
    create_aws_secret,
    S3_SECRET_NAME,
    create_s3_bucket,
)

DEFAULT_APPDB_USER_NAME = "mongodb-ops-manager"

"""
This test checks the backup if no separate S3 Metadata database is created and AppDB is used for this.
Note, that it doesn't check for mongodb backup as it's done in 'e2e_om_ops_manager_backup_restore'" 
"""


@fixture(scope="module")
def s3_bucket(aws_s3_client: AwsS3Client, namespace: str) -> str:
    create_aws_secret(aws_s3_client, S3_SECRET_NAME, namespace)
    yield from create_s3_bucket(aws_s3_client)


@fixture(scope="module")
def ops_manager(
    namespace: str,
    s3_bucket: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup_light.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource["spec"]["backup"]["s3Stores"][0]["s3BucketName"] = s3_bucket

    return resource.create()


@fixture(scope="module")
def oplog_replica_set(ops_manager, namespace) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=OPLOG_RS_NAME,
    ).configure(ops_manager, "development")

    return resource.create()


@mark.e2e_om_ops_manager_backup_liveness_probe
class TestOpsManagerCreation:
    def test_create_om(self, ops_manager: MongoDBOpsManager):
        """ creates a s3 bucket and an OM resource, the S3 configs get created using AppDB. Oplog store is still required. """
        ops_manager.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            msg_regexp="Oplog Store configuration is required for backup",
            timeout=600,
        )

    def test_readiness_probe_is_configured(self, ops_manager: MongoDBOpsManager):
        backup_sts = ops_manager.read_backup_statefulset()
        readiness_probe = backup_sts.spec.template.spec.containers[0].readiness_probe
        assert readiness_probe.period_seconds == 3
        assert readiness_probe.initial_delay_seconds == 1
        assert readiness_probe.success_threshold == 1
        assert readiness_probe.failure_threshold == 3

    def test_liveness_probe_is_configured(self, ops_manager: MongoDBOpsManager):
        backup_sts = ops_manager.read_backup_statefulset()
        liveness_probe = backup_sts.spec.template.spec.containers[0].liveness_probe
        assert liveness_probe.period_seconds == 5
        assert liveness_probe.initial_delay_seconds == 1
        assert liveness_probe.success_threshold == 1
        assert liveness_probe.failure_threshold == 5

    def test_oplog_mdb_created(
        self,
        oplog_replica_set: MongoDB,
    ):
        oplog_replica_set.assert_reaches_phase(Phase.Running)

    def test_add_oplog_config(self, ops_manager: MongoDBOpsManager):
        ops_manager.load()
        ops_manager["spec"]["backup"]["opLogStores"] = [
            {"name": "oplog1", "mongodbResourceRef": {"name": "my-mongodb-oplog"}}
        ]
        ops_manager.update()

        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running,
            timeout=500,
            ignore_errors=True,
        )

    @skip_if_local
    def test_om(
        self,
        ops_manager: MongoDBOpsManager,
        oplog_replica_set: MongoDB,
    ):
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        om_tester.assert_daemon_enabled(ops_manager.backup_daemon_pod_name(), HEAD_PATH)
        om_tester.assert_oplog_stores([new_om_data_store(oplog_replica_set, "oplog1")])

        ops_manager.appdb_status().assert_reaches_phase(Phase.Running, timeout=600)
        ops_manager.assert_appdb_monitoring_group_was_created()


@mark.e2e_om_ops_manager_backup_liveness_probe
def test_backup_daemon_pod_restarts_when_process_is_killed(
    ops_manager: MongoDBOpsManager,
):
    corev1_client = client.CoreV1Api()

    backup_daemon_pod = corev1_client.read_namespaced_pod(
        ops_manager.backup_daemon_pod_name(), ops_manager.namespace
    )

    # ensure the pod has not yet been restarted.
    assert backup_daemon_pod.status.container_statuses[0].restart_count == 0

    # get the process id of the Backup Daemon.
    cmd = ["/opt/scripts/backup-daemon-liveness-probe.sh"]
    process_id = KubernetesTester.run_command_in_pod_container(
        ops_manager.backup_daemon_pod_name(),
        ops_manager.namespace,
        cmd,
    )

    kill_cmd = ["/bin/sh", "-c", f"kill -9 {process_id}"]

    # kill the process, resulting in the liveness probe terminating the backup daemon.
    result = KubernetesTester.run_command_in_pod_container(
        ops_manager.backup_daemon_pod_name(),
        ops_manager.namespace,
        kill_cmd,
    )

    # ensure the process was existed and was terminated successfully.
    assert "No such process" not in result

    def backup_daemon_container_has_restarted():
        try:
            pod = corev1_client.read_namespaced_pod(
                ops_manager.backup_daemon_pod_name(), ops_manager.namespace
            )
            return pod.status.container_statuses[0].restart_count > 0
        except Exception as e:
            print("Error reading pod state: " + str(e))
            return False

    KubernetesTester.wait_until(backup_daemon_container_has_restarted, timeout=3500)


@mark.e2e_om_ops_manager_backup_liveness_probe
def test_backup_daemon_reaches_ready_state(ops_manager: MongoDBOpsManager):
    corev1_client = client.CoreV1Api()

    def backup_daemon_is_ready():
        try:
            pod = corev1_client.read_namespaced_pod(
                ops_manager.backup_daemon_pod_name(), ops_manager.namespace
            )
            return pod.status.container_statuses[0].ready
        except Exception as e:
            print("Error checking if pod is ready: " + str(e))
            return False

    KubernetesTester.wait_until(backup_daemon_is_ready, timeout=300)
