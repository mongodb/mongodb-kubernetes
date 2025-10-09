from typing import Optional

import semver
from kubetester import wait_until
from kubetester.kubetester import ensure_ent_version
from kubetester.kubetester import fixture as yaml_fixture
from kubetester.mongodb import MongoDB
from kubetester.opsmanager import MongoDBOpsManager
from kubetester.phase import Phase
from pytest import fixture, mark
from tests.conftest import (
    assert_log_rotation_backup_monitoring,
    assert_log_rotation_process,
    get_custom_om_version,
    is_multi_cluster,
    setup_log_rotate_for_agents,
)
from tests.opsmanager.om_ops_manager_backup import BLOCKSTORE_RS_NAME, OPLOG_RS_NAME
from tests.opsmanager.withMonitoredAppDB.conftest import enable_multi_cluster_deployment

DEFAULT_APPDB_USER_NAME = "mongodb-ops-manager"
supports_process_log_rotation = semver.VersionInfo.parse(get_custom_om_version()).match(
    ">=7.0.4"
) or semver.VersionInfo.parse(get_custom_om_version()).match(">=6.0.24")


@fixture(scope="module")
def ops_manager(
    namespace: str,
    custom_version: Optional[str],
    custom_appdb_version: str,
) -> MongoDBOpsManager:
    resource: MongoDBOpsManager = MongoDBOpsManager.from_yaml(
        yaml_fixture("om_backup_delete_sts.yaml"), namespace=namespace
    )
    resource.set_version(custom_version)
    resource.set_appdb_version(custom_appdb_version)

    if is_multi_cluster():
        enable_multi_cluster_deployment(resource)

    resource.update()
    return resource


@fixture(scope="module")
def oplog_replica_set(ops_manager, namespace, custom_mdb_version) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=namespace,
        name=OPLOG_RS_NAME,
    ).configure(ops_manager, "development")
    resource.set_version(ensure_ent_version(custom_mdb_version))

    setup_log_rotate_for_agents(resource, supports_process_log_rotation)

    return resource.update()


@fixture(scope="module")
def blockstore_replica_set(
    ops_manager,
    custom_mdb_version,
) -> MongoDB:
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),
        namespace=ops_manager.namespace,
        name=BLOCKSTORE_RS_NAME,
    ).configure(ops_manager, "blockstore")
    resource.set_version(ensure_ent_version(custom_mdb_version))

    return resource.update()


@mark.e2e_om_ops_manager_backup_delete_sts_and_log_rotation
def test_create_om(ops_manager: MongoDBOpsManager):
    ops_manager.om_status().assert_reaches_phase(Phase.Running)


@mark.e2e_om_ops_manager_backup_delete_sts_and_log_rotation
def test_create_backing_replica_sets(oplog_replica_set: MongoDB, blockstore_replica_set: MongoDB):
    oplog_replica_set.assert_reaches_phase(Phase.Running)
    blockstore_replica_set.assert_reaches_phase(Phase.Running)


@mark.e2e_om_ops_manager_backup_delete_sts_and_log_rotation
def test_automation_log_rotation(ops_manager: MongoDBOpsManager):
    config = ops_manager.get_om_tester(project_name="development").get_automation_config_tester()
    processes = config.automation_config["processes"]
    for p in processes:
        assert_log_rotation_process(p, supports_process_log_rotation)


@mark.e2e_om_ops_manager_backup_delete_sts_and_log_rotation
def test_backup_log_rotation(ops_manager: MongoDBOpsManager):
    bvk = ops_manager.get_om_tester(project_name="development").get_backup_config()
    assert_log_rotation_backup_monitoring(bvk)


@mark.e2e_om_ops_manager_backup_delete_sts_and_log_rotation
def test_monitoring_log_rotation(ops_manager: MongoDBOpsManager):
    m = ops_manager.get_om_tester(project_name="development").get_monitoring_config()
    assert_log_rotation_backup_monitoring(m)


@mark.e2e_om_ops_manager_backup_delete_sts_and_log_rotation
def test_backup_statefulset_gets_recreated(
    ops_manager: MongoDBOpsManager,
):
    # Wait for the the backup to be fully running
    ops_manager.backup_status().assert_reaches_phase(Phase.Running)
    ops_manager.load()
    ops_manager["spec"]["backup"]["statefulSet"] = {"spec": {"revisionHistoryLimit": 15}}
    ops_manager.update()

    ops_manager.backup_status().assert_reaches_phase(Phase.Running)

    # the backup statefulset should have been recreated
    def check_backup_sts():
        try:
            ops_manager.read_backup_statefulset()
            return True
        except:
            return False

    wait_until(check_backup_sts, timeout=90, sleep_time=5)
