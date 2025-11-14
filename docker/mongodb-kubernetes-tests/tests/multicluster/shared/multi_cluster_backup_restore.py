import datetime
import time
from typing import List

import kubernetes
import kubernetes.client
from kubernetes import client
from kubetester import (
    create_or_update_configmap,
    get_default_storage_class,
    read_service,
)
from kubetester.kubetester import KubernetesTester
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.omtester import OMTester
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from tests.conftest import (
    assert_data_got_restored,
    update_coredns_hosts,
)

TEST_DATA = {"_id": "unique_id", "name": "John", "address": "Highway 37", "age": 30}

MONGODB_PORT = 30000


HEAD_PATH = "/head/"
OPLOG_RS_NAME = "my-mongodb-oplog"
BLOCKSTORE_RS_NAME = "my-mongodb-blockstore"
USER_PASSWORD = "/qwerty@!#:"


def create_project_config_map(om: MongoDBOpsManager, mdb_name, project_name, client, custom_ca):
    name = f"{mdb_name}-config"
    data = {
        "baseUrl": om.om_status().get_url(),
        "projectName": project_name,
        "sslMMSCAConfigMap": custom_ca,
        "orgId": "",
    }

    create_or_update_configmap(om.namespace, name, data, client)


def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


class TestOpsManagerCreation:
    """
    name: Ops Manager successful creation with backup and oplog stores enabled
    description: |
      Creates an Ops Manager instance with backup enabled. The OM is expected to get to 'Pending' state
      eventually as it will wait for oplog db to be created
    """

    def test_create_om(
        ops_manager: MongoDBOpsManager,
    ):
        ops_manager["spec"]["backup"]["headDB"]["storageClass"] = get_default_storage_class()
        ops_manager["spec"]["backup"]["members"] = 1

        ops_manager.update()

        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            msg_regexp="The MongoDB object .+ doesn't exist",
            timeout=1800,
        )

    def test_daemon_statefulset(
        ops_manager: MongoDBOpsManager,
    ):
        def stateful_set_becomes_ready():
            stateful_set = ops_manager.read_backup_statefulset()
            return stateful_set.status.ready_replicas == 1 and stateful_set.status.current_replicas == 1

        KubernetesTester.wait_until(stateful_set_becomes_ready, timeout=300)

        stateful_set = ops_manager.read_backup_statefulset()
        # pod template has volume mount request
        assert (HEAD_PATH, "head") in (
            (mount.mount_path, mount.name) for mount in stateful_set.spec.template.spec.containers[0].volume_mounts
        )

    def test_backup_daemon_services_created(
        namespace,
        central_cluster_client: kubernetes.client.ApiClient,
    ):
        """Backup creates two additional services for queryable backup"""
        services = client.CoreV1Api(api_client=central_cluster_client).list_namespaced_service(namespace).items

        backup_services = [s for s in services if s.metadata.name.startswith("om-backup")]

        assert len(backup_services) >= 3


class TestBackupDatabasesAdded:
    """name: Creates mongodb resources for oplog and blockstore and waits until OM resource gets to
    running state"""

    def test_backup_mdbs_created(
        oplog_replica_set: MongoDB,
        blockstore_replica_set: MongoDB,
    ):
        """Creates mongodb databases all at once"""
        oplog_replica_set.assert_reaches_phase(Phase.Running)
        blockstore_replica_set.assert_reaches_phase(Phase.Running)

    def test_oplog_user_created(oplog_user: MongoDBUser):
        oplog_user.assert_reaches_phase(Phase.Updated)

    def test_om_failed_oplog_no_user_ref(ops_manager: MongoDBOpsManager):
        """Waits until Backup is in failed state as blockstore doesn't have reference to the user"""
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Failed,
            msg_regexp=".*is configured to use SCRAM-SHA authentication mode, the user "
            "must be specified using 'mongodbUserRef'",
        )

    def test_fix_om(ops_manager: MongoDBOpsManager, oplog_user: MongoDBUser):
        ops_manager.load()
        ops_manager["spec"]["backup"]["opLogStores"][0]["mongodbUserRef"] = {"name": oplog_user.name}
        ops_manager.update()

        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running,
            timeout=200,
            ignore_errors=True,
        )

        assert ops_manager.backup_status().get_message() is None


class TestBackupForMongodb:
    def test_setup_om_connection(
        ops_manager: MongoDBOpsManager,
        central_cluster_client: kubernetes.client.ApiClient,
        member_cluster_clients: List[MultiClusterClient],
    ):
        """
        The base_url makes OM accessible from member clusters via a special interconnected dns address.
        """
        ops_manager.load()
        external_svc_name = ops_manager.external_svc_name()
        svc = read_service(ops_manager.namespace, external_svc_name, api_client=central_cluster_client)
        # we have no hostName, but the ip is resolvable.
        ip = svc.status.load_balancer.ingress[0].ip

        interconnected_field = f"om-backup.{ops_manager.namespace}.interconnected"

        # let's make sure that every client can connect to OM.
        for c in member_cluster_clients:
            update_coredns_hosts(
                host_mappings=[(ip, interconnected_field)],
                api_client=c.api_client,
                cluster_name=c.cluster_name,
            )

        # let's make sure that the operator can connect to OM via that given address.
        update_coredns_hosts(
            host_mappings=[(ip, interconnected_field)],
            api_client=central_cluster_client,
            cluster_name="central-cluster",
        )

        new_address = f"https://{interconnected_field}:8443"
        # updating the central url app setting to point at the external address,
        # this allows agents in other clusters to communicate correctly with this OM instance.
        ops_manager["spec"]["configuration"]["mms.centralUrl"] = new_address
        ops_manager.update()

    def test_mongodb_multi_one_running_state(mongodb_multi_one: MongoDBMulti | MongoDB):
        # we might fail connection in the beginning since we set a custom dns in coredns
        mongodb_multi_one.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=1200)

    def test_add_test_data(mongodb_multi_one_collection):
        mongodb_multi_one_collection.insert_one(TEST_DATA)

    def test_mdb_backed_up(project_one: OMTester):
        project_one.wait_until_backup_snapshots_are_ready(expected_count=1)

    def test_change_mdb_data(mongodb_multi_one_collection):
        now_millis = time_to_millis(datetime.datetime.now())
        print("\nCurrent time (millis): {}".format(now_millis))
        time.sleep(30)
        mongodb_multi_one_collection.insert_one({"foo": "bar"})

    def test_pit_restore(project_one: OMTester):
        now_millis = time_to_millis(datetime.datetime.now())
        print("\nCurrent time (millis): {}".format(now_millis))

        backup_completion_time = project_one.get_latest_backup_completion_time()
        print("\nbackup_completion_time: {}".format(backup_completion_time))

        pit_millis = backup_completion_time + 1500

        print(f"Restoring back to: {pit_millis}")

        project_one.create_restore_job_pit(pit_millis)

    def test_data_got_restored(mongodb_multi_one_collection, mdb_client):
        assert_data_got_restored(TEST_DATA, mongodb_multi_one_collection, timeout=1200)


def time_to_millis(date_time) -> int:
    """https://stackoverflow.com/a/11111177/614239"""
    epoch = datetime.datetime.utcfromtimestamp(0)
    pit_millis = (date_time - epoch).total_seconds() * 1000
    return pit_millis
