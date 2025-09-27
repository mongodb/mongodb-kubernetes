import datetime
import time
from typing import Dict, List, Optional

import kubernetes
import kubernetes.client
import pymongo
import pytest
from kubernetes import client
from kubetester import (
    create_or_update_configmap,
    create_or_update_secret,
    get_default_storage_class,
    read_service,
    try_load,
)
from kubetester.certs import create_ops_manager_tls_certs
from kubetester.kubetester import KubernetesTester, ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.kubetester import skip_if_local
from kubetester.mongodb import MongoDB
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.mongodb_user import MongoDBUser
from kubetester.multicluster_client import MultiClusterClient
from kubetester.omtester import OMTester
from kubetester.operator import Operator
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import (
    assert_data_got_restored,
    update_coredns_hosts,
    wait_for_primary,
)

TEST_DATA = {"_id": "unique_id", "name": "John", "address": "Highway 37", "age": 30}

MONGODB_PORT = 30000

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
        # We need the interconnected certificate since we update coreDNS later with that ip -> domain
        # because our central cluster is not part of the mesh, but we can access the pods via external IPs.
        # Since we are using TLS we need a certificate for a hostname, an IP does not work, hence
        #  f"om-backup.{namespace}.interconnected" -> IP setup below
        additional_domains=[
            "fastdl.mongodb.org",
            f"om-backup.{namespace}.interconnected",
        ],
        api_client=central_cluster_client,
    )


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
def ops_manager(
    namespace: str,
    multi_cluster_issuer_ca_configmap: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    ops_manager_certs: str,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_ops_manager_backup.yaml"), namespace=namespace
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource["spec"]["externalConnectivity"] = {"type": "LoadBalancer"}
    resource["spec"]["security"] = {
        "certsSecretPrefix": "mdb",
        "tls": {"ca": multi_cluster_issuer_ca_configmap},
    }
    # remove s3 config
    del resource["spec"]["backup"]["s3Stores"]

    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    resource.allow_mdb_rc_versions()
    resource.create_admin_secret(api_client=central_cluster_client)

    try_load(resource)

    return resource


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

    resource.configure(
        ops_manager,
        ca_config_map_name=multi_cluster_issuer_ca_configmap,
        api_client=central_cluster_client,
    )

    resource["spec"]["opsManager"]["configMapRef"]["name"] = OPLOG_RS_NAME + "-config"
    resource.set_version(custom_mdb_version)

    resource["spec"]["security"] = {"authentication": {"enabled": True, "modes": ["SCRAM"]}}

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    resource.update()

    return resource


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
    resource.configure(
        ops_manager,
        project_name="blockstore",
        ca_config_map_name=multi_cluster_issuer_ca_configmap,
        api_client=central_cluster_client,
    )

    resource.set_version(custom_mdb_version)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.update()

    return resource


@fixture(scope="module")
def blockstore_user(
    namespace,
    blockstore_replica_set: MongoDB,
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBUser:
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
        api_client=central_cluster_client,
    )

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.update()

    return resource


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

    print(f"\nCreating password for MongoDBUser {resource.name} in secret/{resource.get_secret_name()} ")
    create_or_update_secret(
        KubernetesTester.get_namespace(),
        resource.get_secret_name(),
        {
            "password": USER_PASSWORD,
        },
        api_client=central_cluster_client,
    )

    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.update()
    return resource


@mark.e2e_multi_cluster_backup_restore
def test_deploy_operator(multi_cluster_operator: Operator):
    multi_cluster_operator.assert_is_running()


@mark.e2e_multi_cluster_backup_restore
class TestOpsManagerCreation:
    """
    name: Ops Manager successful creation with backup and oplog stores enabled
    description: |
      Creates an Ops Manager instance with backup enabled. The OM is expected to get to 'Pending' state
      eventually as it will wait for oplog db to be created
    """

    def test_create_om(
        self,
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
        self,
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
        self,
        namespace,
        central_cluster_client: kubernetes.client.ApiClient,
    ):
        """Backup creates two additional services for queryable backup"""
        services = client.CoreV1Api(api_client=central_cluster_client).list_namespaced_service(namespace).items

        backup_services = [s for s in services if s.metadata.name.startswith("om-backup")]

        assert len(backup_services) >= 3


@mark.e2e_multi_cluster_backup_restore
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
        ops_manager["spec"]["backup"]["opLogStores"][0]["mongodbUserRef"] = {"name": oplog_user.name}
        ops_manager.update()

        ops_manager.backup_status().assert_reaches_phase(
            Phase.Running,
            timeout=200,
            ignore_errors=True,
        )

        assert ops_manager.backup_status().get_message() is None


class TestBackupForMongodb:
    @fixture(scope="module")
    def base_url(
        self,
        ops_manager: MongoDBOpsManager,
    ) -> str:
        """
        The base_url makes OM accessible from member clusters via a special interconnected dns address.
        This address only works for member clusters.
        """
        interconnected_field = f"https://om-backup.{ops_manager.namespace}.interconnected"
        new_address = f"{interconnected_field}:8443"

        return new_address

    @fixture(scope="module")
    def project_one(
        self,
        ops_manager: MongoDBOpsManager,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
        base_url: str,
    ) -> OMTester:
        return ops_manager.get_om_tester(
            project_name=f"{namespace}-project-one",
            api_client=central_cluster_client,
            base_url=base_url,
        )

    @fixture(scope="function")
    def mdb_client(self, mongodb_multi_one: MongoDBMulti):
        return pymongo.MongoClient(
            mongodb_multi_one.tester(port=MONGODB_PORT).cnx_string,
            **mongodb_multi_one.tester(port=MONGODB_PORT).default_opts,
            readPreference="primary",  # let's read from the primary and not stale data from the secondary
        )

    @fixture(scope="function")
    def mongodb_multi_one_collection(self, mdb_client):
        # Ensure primary is available before proceeding
        wait_for_primary(mdb_client)

        return mdb_client["testdb"]["testcollection"]

    @fixture(scope="module")
    def mongodb_multi_one(
        self,
        ops_manager: MongoDBOpsManager,
        multi_cluster_issuer_ca_configmap: str,
        central_cluster_client: kubernetes.client.ApiClient,
        namespace: str,
        member_cluster_names: List[str],
        base_url,
        custom_mdb_version: str,
    ) -> MongoDBMulti:
        resource = MongoDBMulti.from_yaml(
            yaml_fixture("mongodb-multi.yaml"),
            "multi-replica-set-one",
            namespace,
            # the project configmap should be created in the central cluster.
        ).configure(
            ops_manager,
            ca_config_map_name=multi_cluster_issuer_ca_configmap,
            api_client=central_cluster_client,
        )

        resource.set_version(ensure_ent_version(custom_mdb_version))
        resource["spec"]["clusterSpecList"] = [
            {"clusterName": member_cluster_names[0], "members": 2},
            {"clusterName": member_cluster_names[1], "members": 1},
            {"clusterName": member_cluster_names[2], "members": 2},
        ]

        # creating a cluster with backup should work with custom ports
        resource["spec"].update({"additionalMongodConfig": {"net": {"port": MONGODB_PORT}}})

        resource.configure_backup(mode="enabled")
        resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

        return resource.update()

    @mark.e2e_multi_cluster_backup_restore
    def test_setup_om_connection(
        self,
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

    @mark.e2e_multi_cluster_backup_restore
    def test_mongodb_multi_one_running_state(self, mongodb_multi_one: MongoDBMulti):
        # we might fail connection in the beginning since we set a custom dns in coredns
        mongodb_multi_one.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=1200)

    @skip_if_local
    @mark.e2e_multi_cluster_backup_restore
    @pytest.mark.flaky(reruns=100, reruns_delay=6)
    def test_add_test_data(self, mongodb_multi_one_collection):
        mongodb_multi_one_collection.insert_one(TEST_DATA)

    @mark.e2e_multi_cluster_backup_restore
    def test_mdb_backed_up(self, project_one: OMTester):
        project_one.wait_until_backup_snapshots_are_ready(expected_count=1)

    @mark.e2e_multi_cluster_backup_restore
    def test_change_mdb_data(self, mongodb_multi_one_collection):
        now_millis = time_to_millis(datetime.datetime.now())
        print("\nCurrent time (millis): {}".format(now_millis))
        time.sleep(30)
        mongodb_multi_one_collection.insert_one({"foo": "bar"})

    @mark.e2e_multi_cluster_backup_restore
    def test_pit_restore(self, project_one: OMTester):
        now_millis = time_to_millis(datetime.datetime.now())
        print("\nCurrent time (millis): {}".format(now_millis))

        backup_completion_time = project_one.get_latest_backup_completion_time()
        print("\nbackup_completion_time: {}".format(backup_completion_time))

        pit_millis = backup_completion_time + 1500

        print(f"Restoring back to: {pit_millis}")

        project_one.create_restore_job_pit(pit_millis)

    @mark.e2e_multi_cluster_backup_restore
    def test_data_got_restored(self, mongodb_multi_one_collection, mdb_client):
        assert_data_got_restored(TEST_DATA, mongodb_multi_one_collection, timeout=1200)


def time_to_millis(date_time) -> int:
    """https://stackoverflow.com/a/11111177/614239"""
    epoch = datetime.datetime.utcfromtimestamp(0)
    pit_millis = (date_time - epoch).total_seconds() * 1000
    return pit_millis
