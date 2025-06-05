import datetime
import time

import kubernetes.client
import pymongo
from kubetester import create_or_update_configmap, try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.constants import (
    MONGODB_PORT,
    S3_BLOCKSTORE_NAME,
    S3_OPLOG_NAME,
    TEST_DATA,
)
from tests.common.ops_manager.multi_cluster import (
    ops_manager_multi_cluster_with_tls_s3_backups,
)
from tests.conftest import AWS_REGION, assert_data_got_restored
from tests.multicluster.conftest import cluster_spec_list


@fixture(scope="module")
def appdb_member_cluster_names() -> list[str]:
    return ["kind-e2e-cluster-2", "kind-e2e-cluster-3"]


def create_project_config_map(om: MongoDBOpsManager, mdb_name, project_name, client, custom_ca):
    name = f"{mdb_name}-config"
    data = {
        "baseUrl": om.om_status().get_url(),
        "projectName": project_name,
        "sslMMSCAConfigMap": custom_ca,
        "orgId": "",
    }

    create_or_update_configmap(om.namespace, name, data, client)


@fixture(scope="module")
def multi_cluster_s3_replica_set(
    ops_manager,
    namespace,
    central_cluster_client: kubernetes.client.ApiClient,
    appdb_member_cluster_names: list[str],
    custom_mdb_version: str,
) -> MongoDBMulti:
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("mongodb-multi-cluster.yaml"), "multi-replica-set", namespace
    ).configure(ops_manager, "s3metadata", api_client=central_cluster_client)

    resource["spec"]["clusterSpecList"] = cluster_spec_list(appdb_member_cluster_names, [1, 2])
    resource.set_version(ensure_ent_version(custom_mdb_version))
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    yield resource.update()


@fixture(scope="module")
def ops_manager(
    namespace: str,
    s3_bucket_oplog: str,
    s3_bucket_blockstore: str,
    central_cluster_client: kubernetes.client.ApiClient,
    custom_appdb_version: str,
    custom_version: str,
) -> MongoDBOpsManager:
    resource = ops_manager_multi_cluster_with_tls_s3_backups(
        namespace,
        "om-backup-tls-s3",
        central_cluster_client,
        custom_appdb_version,
        s3_bucket_blockstore,
        s3_bucket_oplog,
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

    resource.allow_mdb_rc_versions()
    resource.set_version(custom_version)

    del resource["spec"]["security"]
    del resource["spec"]["applicationDatabase"]["security"]

    if try_load(resource):
        return resource

    return resource


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_multi_cluster_appdb_s3_based_backup_restore
class TestOpsManagerCreation:
    """
    name: Ops Manager successful creation with backup and oplog stores enabled
    description: |
      Creates an Ops Manager instance with backup enabled.
    """

    def test_create_om(
        self,
        ops_manager: MongoDBOpsManager,
    ):
        ops_manager["spec"]["backup"]["members"] = 1
        ops_manager.update()

        ops_manager.appdb_status().assert_reaches_phase(Phase.Running)
        ops_manager.om_status().assert_reaches_phase(Phase.Running)

    def test_om_is_running(
        self,
        ops_manager: MongoDBOpsManager,
        central_cluster_client: kubernetes.client.ApiClient,
    ):
        # at this point AppDB is used as the "metadatastore"
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, ignore_errors=True)
        om_tester = ops_manager.get_om_tester(api_client=central_cluster_client)
        om_tester.assert_healthiness()

    def test_add_metadatastore(
        self,
        multi_cluster_s3_replica_set: MongoDBMulti,
        ops_manager: MongoDBOpsManager,
    ):
        multi_cluster_s3_replica_set.assert_reaches_phase(Phase.Running, timeout=1000)

        # configure metadatastore in om, use dedicate MDB instead of AppDB
        ops_manager.load()
        ops_manager["spec"]["backup"]["s3Stores"][0]["mongodbResourceRef"] = {"name": multi_cluster_s3_replica_set.name}
        ops_manager["spec"]["backup"]["s3OpLogStores"][0]["mongodbResourceRef"] = {
            "name": multi_cluster_s3_replica_set.name
        }
        ops_manager.update()

        ops_manager.om_status().assert_reaches_phase(Phase.Running)
        ops_manager.backup_status().assert_reaches_phase(Phase.Running, ignore_errors=True)

    def test_om_s3_stores(
        self,
        ops_manager: MongoDBOpsManager,
        central_cluster_client: kubernetes.client.ApiClient,
    ):
        om_tester = ops_manager.get_om_tester(api_client=central_cluster_client)
        om_tester.assert_s3_stores([{"id": S3_BLOCKSTORE_NAME, "s3RegionOverride": AWS_REGION}])
        om_tester.assert_oplog_s3_stores([{"id": S3_OPLOG_NAME, "s3RegionOverride": AWS_REGION}])


@mark.e2e_multi_cluster_appdb_s3_based_backup_restore
class TestBackupForMongodb:
    @fixture(scope="module")
    def project_one(
        self,
        ops_manager: MongoDBOpsManager,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
    ) -> OMTester:
        return ops_manager.get_om_tester(
            project_name=f"{namespace}-project-one",
            api_client=central_cluster_client,
        )

    @fixture(scope="module")
    def mongodb_multi_one_collection(self, mongodb_multi_one: MongoDBMulti):
        # we instantiate the pymongo client per test to avoid flakiness as the primary and secondary might swap
        collection = pymongo.MongoClient(
            mongodb_multi_one.tester(port=MONGODB_PORT).cnx_string,
            **mongodb_multi_one.tester(port=MONGODB_PORT).default_opts,
        )["testdb"]

        return collection["testcollection"]

    @fixture(scope="module")
    def mongodb_multi_one(
        self,
        ops_manager: MongoDBOpsManager,
        central_cluster_client: kubernetes.client.ApiClient,
        namespace: str,
        appdb_member_cluster_names: list[str],
        custom_mdb_version: str,
    ) -> MongoDBMulti:
        resource = MongoDBMulti.from_yaml(
            yaml_fixture("mongodb-multi.yaml"),
            "multi-replica-set-one",
            namespace,
            # the project configmap should be created in the central cluster.
        ).configure(ops_manager, f"{namespace}-project-one", api_client=central_cluster_client)

        resource["spec"]["clusterSpecList"] = cluster_spec_list(appdb_member_cluster_names, [1, 2])

        # creating a cluster with backup should work with custom ports
        resource["spec"].update({"additionalMongodConfig": {"net": {"port": MONGODB_PORT}}})
        resource.set_version(ensure_ent_version(custom_mdb_version))

        resource.configure_backup(mode="enabled")
        resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)

        return resource.update()

    def test_mongodb_multi_one_running_state(self, mongodb_multi_one: MongoDBMulti):
        # we might fail connection in the beginning since we set a custom dns in coredns
        mongodb_multi_one.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=600)

    def test_add_test_data(self, mongodb_multi_one_collection):
        max_attempts = 100
        while max_attempts > 0:
            try:
                mongodb_multi_one_collection.insert_one(TEST_DATA)
                return
            except Exception as e:
                print(e)
                max_attempts -= 1
                time.sleep(6)

    def test_mdb_backed_up(self, project_one: OMTester):
        project_one.wait_until_backup_snapshots_are_ready(expected_count=1)

    def test_change_mdb_data(self, mongodb_multi_one_collection):
        now_millis = time_to_millis(datetime.datetime.now())
        print("\nCurrent time (millis): {}".format(now_millis))
        time.sleep(30)
        mongodb_multi_one_collection.insert_one({"foo": "bar"})

    def test_pit_restore(self, project_one: OMTester):
        now_millis = time_to_millis(datetime.datetime.now())
        print("\nCurrent time (millis): {}".format(now_millis))

        pit_datetme = datetime.datetime.now() - datetime.timedelta(seconds=15)
        pit_millis = time_to_millis(pit_datetme)
        print("Restoring back to the moment 15 seconds ago (millis): {}".format(pit_millis))

        project_one.create_restore_job_pit(pit_millis)

    def test_data_got_restored(self, mongodb_multi_one_collection):
        assert_data_got_restored(TEST_DATA, mongodb_multi_one_collection, timeout=1200)


def time_to_millis(date_time) -> int:
    """https://stackoverflow.com/a/11111177/614239"""
    epoch = datetime.datetime.utcfromtimestamp(0)
    pit_millis = (date_time - epoch).total_seconds() * 1000
    return pit_millis
