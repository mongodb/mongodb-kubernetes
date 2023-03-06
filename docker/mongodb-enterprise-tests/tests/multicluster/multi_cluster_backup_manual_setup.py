from typing import Optional, Dict

from kubernetes import client
from kubetester.certs import create_ops_manager_tls_certs
from kubetester import (
    create_configmap,
    get_default_storage_class,
    delete_secret,
    read_service,
)
from kubetester.kubetester import (
    skip_if_local,
    fixture as yaml_fixture,
    KubernetesTester,
)
from kubetester.mongodb import Phase, MongoDB
from kubetester.mongodb_user import MongoDBUser
from kubetester.opsmanager import MongoDBOpsManager

from pytest import mark, fixture
import kubernetes
from kubetester.operator import Operator
from kubetester import create_secret

HEAD_PATH = "/head/"
OPLOG_RS_NAME = "my-mongodb-oplog"
BLOCKSTORE_RS_NAME = "my-mongodb-blockstore"
USER_PASSWORD = "/qwerty@!#:"


@fixture(scope="module")
def ops_manager_certs(
    namespace: str,
    multi_cluster_issuer: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    return create_ops_manager_tls_certs(
        multi_cluster_issuer,
        namespace,
        "om-backup",
        secret_name="mdb-om-backup-cert",
        additional_domains=["fastdl.mongodb.org"],
        api_client=central_cluster_client,
    )


def create_project_config_map(
    om: MongoDBOpsManager, mdb_name, project_name, client, custom_ca
):
    name = f"{mdb_name}-config"
    data = {
        "baseUrl": om.om_status().get_url(),
        "projectName": project_name,
        "sslMMSCAConfigMap": custom_ca,
        "orgId": "",
    }

    create_configmap(om.namespace, name, data, client)


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


def create_om_admin_secret(namespace: str, client: kubernetes.client.ApiClient):
    data = dict(
        Username="test-user",
        Password="@Sihjifutestpass21nnH",
        FirstName="foo",
        LastName="bar",
    )
    try:
        delete_secret(
            namespace=namespace, name="ops-manager-admin-secret", api_client=client
        )
    except kubernetes.client.ApiException as e:
        if e.status == 404:
            pass
        else:
            raise e

    create_secret(
        namespace=namespace,
        name="ops-manager-admin-secret",
        data=data,
        api_client=client,
    )


@fixture(scope="module")
def ops_manager(
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    custom_appdb_version: str,
    ops_manager_certs: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBOpsManager:
    create_om_admin_secret(namespace, central_cluster_client)
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup.yaml"), namespace=namespace
    )

    resource["spec"]["backup"]["headDB"]["storageClass"] = get_default_storage_class()
    resource["spec"]["backup"]["members"] = 1
    resource["spec"]["externalConnectivity"] = {"type": "LoadBalancer"}
    resource["spec"]["security"] = {
        "certsSecretPrefix": "mdb",
        "tls": {"ca": multi_cluster_issuer_ca_configmap},
    }
    # remove s3 config
    del resource["spec"]["backup"]["s3Stores"]
    resource.allow_mdb_rc_versions()

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    yield resource.create()


@fixture(scope="module")
def oplog_replica_set(
    ops_manager,
    namespace,
    custom_mdb_version: str,
    central_cluster_client: kubernetes.client.ApiClient,
    multi_cluster_issuer_ca_configmap: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=OPLOG_RS_NAME,
    )

    create_project_config_map(
        om=ops_manager,
        project_name="development",
        mdb_name=OPLOG_RS_NAME,
        client=central_cluster_client,
        custom_ca=multi_cluster_issuer_ca_configmap,
    )

    resource["spec"]["opsManager"]["configMapRef"]["name"] = "my-mongodb-oplog-config"
    resource["spec"]["credentials"] = "ops-manager-om-backup-admin-key"
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
    multi_cluster_issuer_ca_configmap: str,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=BLOCKSTORE_RS_NAME,
    )

    create_project_config_map(
        om=ops_manager,
        project_name="blockstore",
        mdb_name=BLOCKSTORE_RS_NAME,
        client=central_cluster_client,
        custom_ca=multi_cluster_issuer_ca_configmap,
    )

    resource["spec"]["version"] = custom_mdb_version
    resource["spec"]["opsManager"]["configMapRef"][
        "name"
    ] = "my-mongodb-blockstore-config"
    resource["spec"]["credentials"] = "ops-manager-om-backup-admin-key"
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
    create_secret(
        KubernetesTester.get_namespace(),
        resource.get_secret_name(),
        {
            "password": USER_PASSWORD,
        },
        api_client=central_cluster_client,
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
    create_secret(
        KubernetesTester.get_namespace(),
        resource.get_secret_name(),
        {
            "password": USER_PASSWORD,
        },
        api_client=central_cluster_client,
    )

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    yield resource.create()


@mark.e2e_multi_cluster_om_ops_manager_backup_manual_setup
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_om_ops_manager_backup_manual_setup
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
                stateful_set.status.ready_replicas == 1
                and stateful_set.status.current_replicas == 1
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

        assert len(backup_services) >= 3


@mark.e2e_multi_cluster_om_ops_manager_backup_manual_setup
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


@mark.e2e_multi_cluster_om_ops_manager_backup_manual_setup
def test_update_central_url(
    ops_manager: MongoDBOpsManager,
    namespace: str,
    central_cluster_client: kubernetes.client.ApiClient,
):
    ops_manager.load()
    external_svc_name = ops_manager.external_svc_name()
    svc = read_service(namespace, external_svc_name, api_client=central_cluster_client)
    hostname = svc.status.load_balancer.ingress[0].hostname

    # update the central url app setting to point at the external address
    # this allows agents in other clusters to communicate correctly with this OM instance.
    ops_manager["spec"]["configuration"]["mms.centralUrl"] = f"https://{hostname}:8443"
    ops_manager.update()


# @mark.e2e_multi_cluster_om_ops_manager_backup_manual_setup
# def test_patch_ops_manager_https_certificates
