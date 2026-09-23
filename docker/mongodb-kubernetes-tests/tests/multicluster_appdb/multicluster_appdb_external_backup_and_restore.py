import datetime
import time
from typing import Optional

import kubernetes.client
from kubernetes.client.rest import ApiException
import pymongo
import pytest
from kubetester import try_load
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.common.constants import TEST_DATA
from tests.conftest import assert_data_got_restored
from tests.multicluster_appdb.multicluster_appdb_external_test_helpers import (
    APPDB_NAME,
    appdb_ca_configmap as helper_appdb_ca_configmap,
    appdb_cert_prefix as helper_appdb_cert_prefix,
    appdb_member_cluster_names as default_appdb_member_cluster_names,
    assert_multi_cluster_appdb_statefulset_identity,
    assert_no_internal_appdb_statefulset,
    configure_appdb_backup_mongodb_ops_manager,
    configure_appdb_role_mongodb_multi,
    multi_cluster_appdb_role_resource,
    primary_om_resource,
    read_appdb_connection_url,
)


@fixture(scope="module")
def appdb_member_cluster_names() -> list[str]:
    return default_appdb_member_cluster_names()


@fixture(scope="module")
def meta_om(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
    central_cluster_client: kubernetes.client.ApiClient,
    s3_bucket_blockstore: str,
    s3_bucket_oplog: str,
) -> MongoDBOpsManager:
    resource = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_external_appdb_meta_om.yaml"), name="meta-om", namespace=namespace
    )
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)
    configure_appdb_backup_mongodb_ops_manager(resource, s3_bucket_blockstore, s3_bucket_oplog)
    try_load(resource)
    return resource


@fixture(scope="module")
def primary_om(
    namespace: str, custom_version: Optional[str], central_cluster_client: kubernetes.client.ApiClient
) -> MongoDBOpsManager:
    resource = primary_om_resource(namespace, custom_version)
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@fixture(scope="module")
def appdb_ca_configmap(multi_cluster_issuer_ca_configmap: str) -> str:
    return helper_appdb_ca_configmap(multi_cluster_issuer_ca_configmap)


@fixture(scope="module")
def appdb_cert_prefix(namespace: str, multi_cluster_issuer: str) -> str:
    return helper_appdb_cert_prefix(namespace, multi_cluster_issuer)


@fixture(scope="module")
def external_appdb(
    namespace: str,
    custom_mdb_version: str,
    appdb_cert_prefix: str,
    appdb_ca_configmap: str,
    appdb_member_cluster_names: list[str],
    central_cluster_client: kubernetes.client.ApiClient,
) -> MongoDBMulti:
    resource = multi_cluster_appdb_role_resource(
        namespace,
        custom_mdb_version,
        appdb_cert_prefix,
        appdb_ca_configmap,
        appdb_member_cluster_names,
    )
    resource.configure_backup(mode="enabled")
    resource.api = kubernetes.client.CustomObjectsApi(central_cluster_client)
    try_load(resource)
    return resource


@fixture(scope="module")
def external_appdb_collection(
    primary_om: MongoDBOpsManager,
    appdb_member_cluster_names: list[str],
    issuer_ca_filepath: str,
):
    cnx_string = read_appdb_connection_url(primary_om, appdb_member_cluster_names[0])
    client = pymongo.MongoClient(cnx_string, tlsCAFile=issuer_ca_filepath, serverSelectionTimeoutMS=30000)
    return client["testdb"]["testcollection"]


@fixture(scope="module")
def project_one(meta_om: MongoDBOpsManager) -> OMTester:
    return meta_om.get_om_tester(project_name=f"{APPDB_NAME}-project")


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_om_external_multi_cluster_appdb_backup_and_restore
class TestDeployMetaOpsManager:
    def test_deploy_meta_om(self, meta_om: MongoDBOpsManager):
        meta_om.update()
        meta_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        meta_om.appdb_status().assert_reaches_phase(Phase.Running, timeout=900)
        meta_om.backup_status().assert_reaches_phase(Phase.Running, ignore_errors=True)


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_om_external_multi_cluster_appdb_backup_and_restore
class TestDeployExternalAppDB:
    def test_create_appdb_role_mongodb(
        self,
        external_appdb: MongoDBMulti,
        meta_om: MongoDBOpsManager,
        namespace: str,
        appdb_ca_configmap: str,
    ):
        configure_appdb_role_mongodb_multi(external_appdb, meta_om, namespace, appdb_ca_configmap)
        external_appdb.update()
        external_appdb.assert_reaches_phase(Phase.Running, timeout=900)

    def test_create_om_with_ref_and_no_internal_appdb(
        self,
        primary_om: MongoDBOpsManager,
        namespace: str,
        central_cluster_client: kubernetes.client.ApiClient,
    ):
        primary_om.update()
        primary_om.om_status().assert_reaches_phase(Phase.Running, timeout=900)
        primary_om.appdb_status().assert_reaches_phase(Phase.Disabled, timeout=600)

        with pytest.raises(ApiException, match=r"NotFound|not found"):
            assert_no_internal_appdb_statefulset(namespace, APPDB_NAME, central_cluster_client)

    def test_external_appdb_statefulsets_are_adopted(
        self,
        external_appdb: MongoDBMulti,
        primary_om: MongoDBOpsManager,
        appdb_member_cluster_names: list[str],
    ):
        assert_multi_cluster_appdb_statefulset_identity(
            external_appdb,
            appdb_member_cluster_names,
        )


@mark.usefixtures("multi_cluster_operator")
@mark.e2e_om_external_multi_cluster_appdb_backup_and_restore
class TestBackupAndRestoreExternalAppDB:
    @pytest.mark.flaky(reruns=100, reruns_delay=6)
    def test_add_test_data(self, external_appdb_collection):
        external_appdb_collection.insert_one(TEST_DATA)

    def test_appdb_backed_up(self, project_one: OMTester):
        project_one.wait_until_backup_snapshots_are_ready(expected_count=1)

    def test_change_appdb_data(self, external_appdb_collection):
        now_millis = int(datetime.datetime.now(tz=datetime.timezone.utc).timestamp() * 1_000)
        print(f"\nCurrent time (millis): {now_millis}")
        time.sleep(30)
        external_appdb_collection.insert_one({"foo": "bar"})

    def test_pit_restore(self, project_one: OMTester):
        backup_completion_time = project_one.get_latest_backup_completion_time()
        print(f"\nbackup_completion_time: {backup_completion_time}")

        pit_millis = backup_completion_time + 1500
        print(f"Restoring back to: {pit_millis}")

        project_one.create_restore_job_pit(pit_millis)

    def test_data_got_restored(self, external_appdb_collection):
        assert_data_got_restored(TEST_DATA, external_appdb_collection, timeout=1200)

    def test_external_appdb_statefulsets_are_still_adopted(
        self,
        external_appdb: MongoDBMulti,
        primary_om: MongoDBOpsManager,
        appdb_member_cluster_names: list[str],
    ):
        assert_multi_cluster_appdb_statefulset_identity(
            external_appdb,
            appdb_member_cluster_names,
        )
