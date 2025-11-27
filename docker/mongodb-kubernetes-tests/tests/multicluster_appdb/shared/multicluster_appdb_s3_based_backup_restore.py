import datetime
import time

import kubernetes.client
import pymongo
import pytest
from kubetester import create_or_update_configmap
from kubetester.mongodb_multi import MongoDBMulti
from kubetester.omtester import OMTester
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from tests.common.constants import (
    S3_BLOCKSTORE_NAME,
    S3_OPLOG_NAME,
    TEST_DATA,
)
from tests.conftest import assert_data_got_restored
from tests.constants import AWS_REGION


def create_project_config_map(om: MongoDBOpsManager, mdb_name, project_name, client, custom_ca):
    name = f"{mdb_name}-config"
    data = {
        "baseUrl": om.om_status().get_url(),
        "projectName": project_name,
        "sslMMSCAConfigMap": custom_ca,
        "orgId": "",
    }

    create_or_update_configmap(om.namespace, name, data, client)


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


class TestBackupForMongodb:

    def test_mongodb_multi_one_running_state(self, mongodb_multi_one: MongoDBMulti):
        # we might fail connection in the beginning since we set a custom dns in coredns
        mongodb_multi_one.assert_reaches_phase(Phase.Running, ignore_errors=True, timeout=600)

    @pytest.mark.flaky(reruns=100, reruns_delay=6)
    def test_add_test_data(self, mongodb_multi_one_collection):
        mongodb_multi_one_collection.insert_one(TEST_DATA)

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
