import datetime
import time

import kubernetes.client
import pymongo
import pytest
from kubetester import create_or_update_configmap, try_load
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pymongo.errors import ServerSelectionTimeoutError
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
from tests.conftest import assert_data_got_restored
from tests.constants import AWS_REGION
from tests.multicluster.conftest import cluster_spec_list
from tests.multicluster_appdb.shared import (
    multicluster_appdb_s3_based_backup_restore as testhelper,
)


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
        yaml_fixture("mongodbmulticluster-multi-cluster.yaml"), "multi-replica-set", namespace
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
@mark.e2e_mongodbmulticluster_multi_cluster_appdb_s3_based_backup_restore
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
        testhelper.TestOpsManagerCreation.test_create_om(self, ops_manager)

    def test_om_is_running(
        self,
        ops_manager: MongoDBOpsManager,
        central_cluster_client: kubernetes.client.ApiClient,
    ):
        testhelper.TestOpsManagerCreation.test_om_is_running(self, ops_manager, central_cluster_client)

    def test_add_metadatastore(
        self,
        multi_cluster_s3_replica_set: MongoDBMulti,
        ops_manager: MongoDBOpsManager,
    ):
        testhelper.TestOpsManagerCreation.test_add_metadatastore(self, multi_cluster_s3_replica_set, ops_manager)

    def test_om_s3_stores(
        self,
        ops_manager: MongoDBOpsManager,
        central_cluster_client: kubernetes.client.ApiClient,
    ):
        testhelper.TestOpsManagerCreation.test_om_s3_stores(self, ops_manager, central_cluster_client)


@mark.e2e_mongodbmulticluster_multi_cluster_appdb_s3_based_backup_restore
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
            yaml_fixture("mongodbmulticluster-multi.yaml"),
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
        testhelper.TestBackupForMongodb.test_mongodb_multi_one_running_state(self, mongodb_multi_one)

    @pytest.mark.flaky(reruns=100, reruns_delay=6)
    def test_add_test_data(self, mongodb_multi_one_collection):
        testhelper.TestBackupForMongodb.test_add_test_data(self, mongodb_multi_one_collection)

    def test_mdb_backed_up(self, project_one: OMTester):
        testhelper.TestBackupForMongodb.test_mdb_backed_up(self, project_one)

    def test_change_mdb_data(self, mongodb_multi_one_collection):
        testhelper.TestBackupForMongodb.test_change_mdb_data(self, mongodb_multi_one_collection)

    def test_pit_restore(self, project_one: OMTester):
        testhelper.TestBackupForMongodb.test_pit_restore(self, project_one)

    def test_data_got_restored(self, mongodb_multi_one_collection):
        testhelper.TestBackupForMongodb.test_data_got_restored(self, mongodb_multi_one_collection)
