from operator import attrgetter
from typing import Optional, Dict, Callable

from kubernetes import client
from kubetester import get_default_storage_class
from kubetester.kubetester import (
    skip_if_local,
    fixture as yaml_fixture,
    KubernetesTester,
)
from kubetester.mongodb import Phase, MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.opsmanager import MongoDBOpsManager

from pytest import mark, fixture, skip
from tests.opsmanager.conftest import ensure_ent_version
import kubernetes
from tests.opsmanager.conftest import custom_appdb_version
from kubetester.operator import Operator
from kubetester import create_secret

HEAD_PATH = "/head/"
OPLOG_RS_NAME = "my-mongodb-oplog"
BLOCKSTORE_RS_NAME = "my-mongodb-blockstore"
USER_PASSWORD = "/qwerty@!#:"


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
def om_admin_secret(
    namespace: str, central_cluster_client: kubernetes.client.ApiClient
):
    data = dict(
        Username="test-user",
        Password="@Sihjifutestpass21nnH",
        FirstName="foo",
        LastName="bar",
    )
    create_secret(
        namespace=namespace,
        name="ops-manager-admin-secret",
        data=data,
        api_client=central_cluster_client,
    )


@fixture(scope="module")
def ops_manager(
    om_admin_secret,
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBOpsManager:

    # create ops-manager-admin-secret
    om_admin_secret
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup.yaml"), namespace=namespace
    )

    resource["spec"]["backup"]["headDB"]["storageClass"] = get_default_storage_class()
    resource["spec"]["backup"]["members"] = 2

    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    yield resource.create()


@fixture(scope="module")
def oplog_replica_set(
    ops_manager,
    namespace,
    custom_mdb_version: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=OPLOG_RS_NAME,
    ).configure(ops_manager, "development")

    resource["spec"]["version"] = custom_mdb_version

    resource["spec"]["security"] = {
        "authentication": {"enabled": True, "modes": ["SCRAM"]}
    }

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    yield resource.create()


@fixture(scope="module")
def blockstore_replica_set(
    ops_manager,
    namespace,
    custom_mdb_version: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=BLOCKSTORE_RS_NAME,
    ).configure(ops_manager, "blockstore")

    resource["spec"]["version"] = custom_mdb_version

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    yield resource.create()


@fixture(scope="module")
def blockstore_user(
    namespace,
    blockstore_replica_set: MongoDB,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBUser:
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

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    yield resource.create()


@fixture(scope="module")
def oplog_user(
    namespace,
    oplog_replica_set: MongoDB,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBUser:
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

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    yield resource.create()


@mark.e2e_multi_cluster_om_ops_manager_backup
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_om_ops_manager_backup
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

    def test_create_om(self, ops_manager: MongoDBOpsManager):
        ops_manager.backup_status().assert_reaches_phase(
            Phase.Pending,
            msg_regexp="The MongoDB object .+ doesn't exist",
            timeout=900,
        )

    def test_daemon_statefulset(
        self,
        ops_manager: MongoDBOpsManager,
        central_cluster_client: kubernetes.client.ApiClient,
    ):
        def stateful_set_becomes_ready():
            stateful_set = ops_manager.read_backup_statefulset(central_cluster_client)
            return (
                stateful_set.status.ready_replicas == 2
                and stateful_set.status.current_replicas == 2
            )

        KubernetesTester.wait_until(stateful_set_becomes_ready, timeout=300)

        stateful_set = ops_manager.read_backup_statefulset(central_cluster_client)
        # pod template has volume mount request
        assert (HEAD_PATH, "head") in (
            (mount.mount_path, mount.name)
            for mount in stateful_set.spec.template.spec.containers[0].volume_mounts
        )

    def test_backup_daemon_services_created(
        self,
        namespace,
        central_cluster_client: kubernetes.client.ApiClient,
    ):
        """Backup creates two additional services for queryable backup"""
        services = (
            client.CoreV1Api(api_client=central_cluster_client)
            .list_namespaced_service(namespace)
            .items
        )

        backup_services = [
            s for s in services if s.metadata.name.startswith("om-backup")
        ]

        assert len(backup_services) >= 4


@mark.e2e_multi_cluster_om_ops_manager_backup
class TestBackupDatabasesAdded:
    """name: Creates mongodb resources for oplog and blockstore and waits until OM resource gets to
    running state"""

    def test_backup_mdbs_created(
        self,
        oplog_replica_set: MongoDB,
        blockstore_replica_set: MongoDB,
    ):
        """Creates mongodb databases all at once"""
        oplog_replica_set.assert_reaches_phase(Phase.Running)
        blockstore_replica_set.assert_reaches_phase(Phase.Running)

    def test_oplog_user_created(self, oplog_user: MongoDBUser):
        oplog_user.assert_reaches_phase(Phase.Updated)

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
        ops_manager: MongoDBOpsManager,
        oplog_replica_set: MongoDB,
        blockstore_replica_set: MongoDB,
        oplog_user: MongoDBUser,
    ):
        om_tester = ops_manager.get_om_tester()
        om_tester.assert_healthiness()
        # Nothing has changed for daemon

        for pod_name in ops_manager.backup_daemon_pods_names():
            om_tester.assert_daemon_enabled(pod_name, HEAD_PATH)

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


def reaches_backup_status(expected_status: str) -> Callable[[MongoDB], bool]:
    def _fn(mdb: MongoDB):
        try:
            return mdb["status"]["backup"]["statusName"] == expected_status
        except KeyError:
            return False

    return _fn
